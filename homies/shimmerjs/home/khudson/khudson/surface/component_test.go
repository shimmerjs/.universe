package surface

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/shimmerjs/khudson/khudson/internal/config"
	"github.com/shimmerjs/khudson/khudson/provider"
)

func TestComponentUsesIntegratedBodyAndLocalActions(t *testing.T) {
	w := config.Widget{ID: "sessions", Title: "Sessions", Render: config.Render{Kind: "native", Module: "claude-panel"}}
	c := NewComponent(w)
	if cmd := c.Update(tea.WindowSizeMsg{Width: 100, Height: 24}); cmd != nil {
		t.Fatal("component attempted host I/O on resize")
	}
	c.Data(module.Data{Title: "Live", Rows: []module.Row{{Kind: module.RowText, Text: "hello", Act: []string{"focus", "one"}}}}, nil)
	c.model.now = time.Unix(100, 0)
	standalone := c.View().Content
	c.model.component = false
	integrated := c.View().Content
	a := strings.Split(standalone, "\n")
	b := strings.Split(integrated, "\n")
	if strings.Join(a[:len(a)-1], "\n") != strings.Join(b[:len(b)-1], "\n") {
		t.Fatal("standalone body diverges from integrated rendering")
	}
	if len(c.model.hits) == 0 {
		t.Fatal("shared body has no actions")
	}
	c.model.sendRowAct(w.ID, []string{"focus", "one"})
	actions := c.Actions()
	if len(actions) != 1 || actions[0].Argv[1] != "one" || len(c.Actions()) != 0 {
		t.Fatal("semantic action sink failed")
	}
	if c.model.busConn != nil || c.model.session != nil {
		t.Fatal("component owns a render-bus connection")
	}
}
