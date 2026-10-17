// Frame-locked request batches, the transport half of the webcam FX
// path. goobs v1.9.0 speaks opcodes 0-7 only: RequestBatch (op 8) and
// RequestBatchResponse (op 9) have no Go type there, and its read loop
// drops an op 9 on the floor -- so a batch rides its own short-lived raw
// connection with the same handshake, which also matches the
// one-connection-per-operation discipline the rest of the OBS transport
// already keeps.
//
// SERIAL_FRAME is the whole reason to batch: OBS executes one request
// per rendered frame, so a Sleep pseudo-request paces a transform path
// against the video clock. The same path sent as N round trips would be
// paced by websocket jitter instead, which is what an eased move cannot
// tolerate.
package renocontrol

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	renocore "github.com/shimmerjs/khudson/khudson/internal/reno"
)

// obs-websocket protocol constants: the opcodes goobs leaves unhandled,
// the RPC version this client speaks, and SERIAL_FRAME's execution id.
const (
	opHello         = 0
	opIdentify      = 1
	opIdentified    = 2
	opRequestBatch  = 8
	opBatchResponse = 9
	obsRPCVersion   = 1
	execSerialFrame = 1
)

type wsEnvelope struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d"`
}

type wsHello struct {
	RPCVersion     int `json:"rpcVersion"`
	Authentication struct {
		Challenge string `json:"challenge"`
		Salt      string `json:"salt"`
	} `json:"authentication"`
}

type wsIdentify struct {
	RPCVersion         int    `json:"rpcVersion"`
	Authentication     string `json:"authentication,omitempty"`
	EventSubscriptions int    `json:"eventSubscriptions"`
}

type wsBatch struct {
	RequestID     string               `json:"requestId"`
	HaltOnFailure bool                 `json:"haltOnFailure"`
	ExecutionType int                  `json:"executionType"`
	Requests      []renocore.FXRequest `json:"requests"`
}

type wsBatchResponse struct {
	RequestID string `json:"requestId"`
	Results   []struct {
		RequestType string `json:"requestType"`
		Status      struct {
			Result  bool   `json:"result"`
			Code    int    `json:"code"`
			Comment string `json:"comment"`
		} `json:"requestStatus"`
	} `json:"results"`
}

// runFrameBatch dials, identifies, ships reqs as one SERIAL_FRAME batch,
// and waits for the batch response. budget bounds the whole exchange --
// it has to cover the batch's own playback time, since the response only
// arrives once OBS has run the last request.
func runFrameBatch(host, password, id string, reqs []renocore.FXRequest, budget time.Duration) error {
	if len(reqs) == 0 {
		return nil
	}
	endpoint := strings.TrimSpace(host)
	if endpoint == "" {
		return fmt.Errorf("obs batch: empty host")
	}
	if !strings.Contains(endpoint, "://") {
		endpoint = "ws://" + endpoint
	}
	dialer := *websocket.DefaultDialer
	dialer.HandshakeTimeout = 5 * time.Second
	conn, _, err := dialer.Dial(endpoint, http.Header{"User-Agent": []string{"khudson-reno"}})
	if err != nil {
		return fmt.Errorf("obs batch at %s: %w", host, err)
	}
	defer conn.Close()
	deadline := time.Now().Add(budget)
	if err := conn.SetReadDeadline(deadline); err != nil {
		return err
	}
	if err := conn.SetWriteDeadline(deadline); err != nil {
		return err
	}

	var hello wsHello
	if err := readOp(conn, opHello, &hello); err != nil {
		return fmt.Errorf("obs batch hello: %w", err)
	}
	ident := wsIdentify{RPCVersion: hello.RPCVersion}
	if ident.RPCVersion == 0 {
		ident.RPCVersion = obsRPCVersion
	}
	if hello.Authentication.Challenge != "" {
		ident.Authentication = authSecret(password, hello.Authentication.Salt, hello.Authentication.Challenge)
	}
	if err := writeOp(conn, opIdentify, ident); err != nil {
		return fmt.Errorf("obs batch identify: %w", err)
	}
	if err := readOp(conn, opIdentified, &struct{}{}); err != nil {
		return fmt.Errorf("obs batch identify: %w", err)
	}

	batch := wsBatch{
		RequestID: id, HaltOnFailure: true,
		ExecutionType: execSerialFrame, Requests: reqs,
	}
	if err := writeOp(conn, opRequestBatch, batch); err != nil {
		return fmt.Errorf("obs batch send: %w", err)
	}
	var resp wsBatchResponse
	if err := readOp(conn, opBatchResponse, &resp); err != nil {
		return fmt.Errorf("obs batch response: %w", err)
	}
	for i, r := range resp.Results {
		if r.Status.Result {
			continue
		}
		// haltOnFailure means the tail never ran; name the request that
		// stopped it rather than the batch as a whole
		detail := r.Status.Comment
		if detail == "" {
			detail = fmt.Sprintf("code %d", r.Status.Code)
		}
		return fmt.Errorf("obs batch request %d (%s): %s", i, r.RequestType, detail)
	}
	if len(resp.Results) != len(reqs) {
		return fmt.Errorf("obs batch ran %d of %d requests", len(resp.Results), len(reqs))
	}
	return nil
}

// authSecret is obs-websocket's two-round challenge: base64 sha256 of
// password+salt, then base64 sha256 of that plus the challenge.
func authSecret(password, salt, challenge string) string {
	h := sha256.Sum256([]byte(password + salt))
	secret := base64.StdEncoding.EncodeToString(h[:])
	a := sha256.Sum256([]byte(secret + challenge))
	return base64.StdEncoding.EncodeToString(a[:])
}

func writeOp(conn *websocket.Conn, op int, d any) error {
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	return conn.WriteJSON(wsEnvelope{Op: op, D: raw})
}

// readOp reads until the wanted opcode arrives, skipping anything else
// the server volunteers (events are unsubscribed, but the protocol does
// not promise silence).
func readOp(conn *websocket.Conn, op int, out any) error {
	for {
		var env wsEnvelope
		if err := conn.ReadJSON(&env); err != nil {
			return err
		}
		if env.Op != op {
			continue
		}
		return json.Unmarshal(env.D, out)
	}
}
