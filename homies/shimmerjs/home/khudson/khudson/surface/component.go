package surface

import (
	"fmt"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/shimmerjs/khudson/khudson/internal/config"
	"github.com/shimmerjs/khudson/khudson/provider"
)

// Component exposes the same widget composition and interaction state used by
// the integrated dock. It performs no socket, provider, or subprocess I/O.
// Hosts supply data, drive Update, and execute the returned semantic actions.
type Component struct {
	model   *model
	widget  string
	actions []ComponentAction
}
type ComponentAction struct {
	Widget string
	Argv   []string
	URL    string
}

func NewComponent(widget config.Widget) *Component {
	if widget.ID == "" {
		widget.ID = widget.Render.Module
	}
	cfg := &config.Config{Layout: "component", Widgets: map[string]config.Widget{widget.ID: widget}, Layouts: map[string]config.Layout{
		"component": {Kind: "home", Regions: []config.Region{{Widget: widget.ID, Edge: "fill"}}},
	}}
	c := &Component{widget: widget.ID}
	c.model = &model{component: true, cfg: cfg, layout: cfg.Layout, now: time.Now(), widgetData: map[string]module.Data{}, widgetErr: map[string]string{}, widgetStale: map[string]bool{}, sty: buildStyles(day)}
	c.model.actionSink = func(id string, argv []string) { c.enqueue(ComponentAction{Widget: id, Argv: slices.Clone(argv)}) }
	c.model.openURL = func(url string) { c.enqueue(ComponentAction{Widget: widget.ID, URL: url}) }
	return c
}
func (c *Component) enqueue(action ComponentAction) {
	if len(c.actions) >= 32 {
		c.model.lastGst = "action queue full"
		return
	}
	c.actions = append(c.actions, action)
}
func (c *Component) Init() tea.Cmd              { return nil }
func (c *Component) Update(msg tea.Msg) tea.Cmd { _, cmd := c.model.Update(msg); return cmd }
func (c *Component) View() tea.View             { return c.model.View() }
func (c *Component) Actions() []ComponentAction { a := c.actions; c.actions = nil; return a }
func (c *Component) Status(status string)       { c.model.lastGst = status; c.model.homeCache.ok = false }
func (c *Component) Data(data module.Data, err error) {
	c.model.now = time.Now()
	c.model.receiveWidgetData(c.widget, data, err)
}
func (c *Component) Theme(colors map[string]string) {
	c.model.palette = palette(colors)
	c.model.sty = buildStyles(c.model.palette)
	c.model.rowsOK = false
	c.model.homeCache.ok = false
}
func (m *model) receiveWidgetData(widget string, data module.Data, err error) {
	m.invalidateHome(widget)
	if err != nil {
		m.widgetErr[widget] = err.Error()
		return
	}
	delete(m.widgetErr, widget)
	m.widgetData[widget] = data
}
func (m *model) renderComponentStrip() string {
	status := m.lastGst
	if status == "" {
		status = "q / ctrl+c: exit"
	}
	return fitCell(fmt.Sprintf(" %s", status), m.width)
}
