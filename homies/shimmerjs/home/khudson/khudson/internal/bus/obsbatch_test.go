package bus

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	renocore "github.com/shimmerjs/khudson/khudson/internal/reno"
)

// fakeObsWS is an obs-websocket server stub: it speaks the handshake and
// answers one batch. results says how each request fared (nil = all ok).
type fakeObsWS struct {
	salt, challenge string
	auth            string
	batch           wsBatch
	results         []bool
}

func (s *fakeObsWS) start(t *testing.T) string {
	t.Helper()
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		hello := wsHello{RPCVersion: 1}
		hello.Authentication.Salt, hello.Authentication.Challenge = s.salt, s.challenge
		if err := writeOp(conn, opHello, hello); err != nil {
			return
		}
		var ident wsIdentify
		if err := readOp(conn, opIdentify, &ident); err != nil {
			return
		}
		s.auth = ident.Authentication
		if err := writeOp(conn, opIdentified, map[string]any{"negotiatedRpcVersion": 1}); err != nil {
			return
		}
		if err := readOp(conn, opRequestBatch, &s.batch); err != nil {
			return
		}
		resp := map[string]any{"requestId": s.batch.RequestID}
		var results []map[string]any
		for i, req := range s.batch.Requests {
			ok := true
			if s.results != nil {
				ok = s.results[i]
			}
			results = append(results, map[string]any{
				"requestType": req.Type,
				"requestStatus": map[string]any{
					"result": ok, "code": 100, "comment": "",
				},
			})
			if !ok {
				break // haltOnFailure
			}
		}
		resp["results"] = results
		writeOp(conn, opBatchResponse, resp)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

func fxRequests() []renocore.FXRequest {
	return []renocore.FXRequest{
		{Type: "SetSceneItemEnabled", Data: map[string]any{"sceneItemEnabled": true}},
		{Type: "Sleep", Data: map[string]any{"sleepFrames": 2}},
		{Type: "SetSceneItemTransform", Data: map[string]any{"sceneItemId": 4}},
	}
}

// The batch goes out as ONE authenticated SERIAL_FRAME message: that
// execution type is what makes OBS run a request per rendered frame, so
// the Sleep pseudo-requests pace the path.
func TestFrameBatchHandshakeAndExecutionType(t *testing.T) {
	srv := &fakeObsWS{salt: "s41t", challenge: "ch4l"}
	host := srv.start(t)
	if err := runFrameBatch(host, "hunter2", "batch-1", fxRequests(), 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if want := authSecret("hunter2", "s41t", "ch4l"); srv.auth != want {
		t.Errorf("auth = %q, want %q", srv.auth, want)
	}
	if srv.batch.ExecutionType != execSerialFrame || !srv.batch.HaltOnFailure {
		t.Errorf("batch = %+v", srv.batch)
	}
	if srv.batch.RequestID != "batch-1" || len(srv.batch.Requests) != 3 {
		t.Errorf("batch = %+v", srv.batch)
	}
	// the request data survives the trip verbatim -- the plan is the wire
	if srv.batch.Requests[1].Data["sleepFrames"] != float64(2) {
		t.Errorf("sleep = %v", srv.batch.Requests[1].Data)
	}
}

// An authless server sends no challenge, and the client must not invent
// one.
func TestFrameBatchWithoutAuth(t *testing.T) {
	srv := &fakeObsWS{}
	host := srv.start(t)
	if err := runFrameBatch(host, "", "b", fxRequests(), 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if srv.auth != "" {
		t.Errorf("auth = %q on an authless server", srv.auth)
	}
}

// haltOnFailure means the tail never ran, so the error names the request
// that stopped the batch instead of the batch as a whole.
func TestFrameBatchFailureNamesTheRequest(t *testing.T) {
	srv := &fakeObsWS{results: []bool{true, false}}
	host := srv.start(t)
	err := runFrameBatch(host, "", "b", fxRequests(), 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "request 1 (Sleep)") {
		t.Fatalf("err = %v", err)
	}
}

func TestFrameBatchEmptyAndBadHost(t *testing.T) {
	if err := runFrameBatch("127.0.0.1:1", "", "b", nil, time.Second); err != nil {
		t.Errorf("an empty batch must not dial: %v", err)
	}
	if err := runFrameBatch("", "", "b", fxRequests(), time.Second); err == nil {
		t.Error("empty host must fail")
	}
}

// The envelope is the obs-websocket op/d shape; a wrong opcode would be
// silently skipped by readOp, so pin it.
func TestBatchEnvelopeShape(t *testing.T) {
	raw, err := json.Marshal(wsEnvelope{Op: opRequestBatch, D: json.RawMessage(`{"a":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"op":8,"d":{"a":1}}` {
		t.Errorf("envelope = %s", raw)
	}
}
