package surface

import (
	"crypto/rand"

	tea "charm.land/bubbletea/v2"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
)

func (m *model) connectBus(gen int, socket string, cols, rows, pcols, prows int) tea.Cmd {
	if m.session == nil {
		return nil
	}
	return m.session.Connect(gen, socket, proto.Msg{
		Type: proto.TypeHello, Role: proto.RoleDock,
		Protocol: proto.Version, Capabilities: &proto.ViewCapabilities{Layouts: []string{"home", "keyboard"}, Edge: m.opts.Edge},
		Cols: cols, Rows: rows, PanelCols: pcols, PanelRows: prows, Layout: m.layout, Hidden: m.visibility.Hidden(),
	})
}

func (m *model) sendBus(msg proto.Msg) error {
	msg.ViewID, msg.ViewGeneration = m.viewID, m.viewGeneration
	if msg.Type == proto.TypeRowAct || msg.Type == proto.TypeAction || msg.Type == proto.TypeCtl {
		msg.OperationID = rand.Text()
	}
	var err error
	if m.session != nil {
		err = m.session.Send(msg, m.busConn)
	} else {
		err = m.busConn.Send(msg)
	}
	if err != nil {
		m.lastGst = "bus send failed: " + err.Error()
	}
	return err
}
