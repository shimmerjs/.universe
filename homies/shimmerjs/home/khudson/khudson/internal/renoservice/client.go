package renoservice

import (
	"context"
	"encoding/json"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/proto"
	"github.com/shimmerjs/khudson/khudson/provider"
)

type Client struct{ Socket string }

func New() *Client           { socket, _ := Socket(); return &Client{Socket: socket} }
func (*Client) Name() string { return "reno" }
func (*Client) Essential()   {}
func (c *Client) Poll(ctx context.Context, params map[string]any) (module.Data, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return module.Data{}, err
	}
	resp, err := Call(ctx, c.Socket, proto.Msg{Type: "poll", Data: raw})
	var data module.Data
	if err == nil {
		err = json.Unmarshal(resp.Data, &data)
	}
	return data, err
}
func (*Client) HandleAct([]string) bool { return false }
func (c *Client) Act(ctx context.Context, argv []string, op module.Operation) error {
	_, err := Call(ctx, c.Socket, proto.Msg{Type: proto.TypeRowAct, Widget: "reno", Argv: argv, OperationID: op.ID, ViewID: op.ViewID, ViewGeneration: op.Generation})
	return err
}
func (c *Client) HandleCtl(args []string) (any, []module.Effect, error) {
	return c.HandleCtlOperation(args, module.Operation{})
}
func (c *Client) HandleCtlOperation(args []string, op module.Operation) (any, []module.Effect, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	resp, err := Call(ctx, c.Socket, proto.Msg{Type: proto.TypeCtl, Cmd: "reno", Args: args, OperationID: op.ID, ViewID: op.ViewID, ViewGeneration: op.Generation})
	return json.RawMessage(resp.Data), nil, err
}
