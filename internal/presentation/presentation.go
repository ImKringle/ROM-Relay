// Package presentation owns all operator-facing CLI wording and styling.
package presentation

import (
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/jedib0t/go-pretty/v6/progress"
	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/jedib0t/go-pretty/v6/text"
)

type Presenter struct {
	Theme       string
	Out, Err    io.Writer
	Interactive bool
	live        LiveSink
	// lineMu serializes writes to Out/Err. Callers that parallelize background
	// work (ROM hashing, 3DS conversion) call Info/Success/Warning/Error from
	// multiple goroutines at once; without this, concurrent fmt.Fprintf calls
	// can interleave mid-line in the log output.
	lineMu sync.Mutex
}

type LiveSink interface {
	SetLiveJob(key, message string, done, total int64, active bool)
}

func New(theme string, out, err io.Writer) *Presenter {
	if theme != "corn" {
		theme = "default"
	}
	interactive := false
	if f, ok := out.(*os.File); ok {
		if st, e := f.Stat(); e == nil {
			interactive = st.Mode()&os.ModeCharDevice != 0
		}
	}
	return &Presenter{Theme: theme, Out: out, Err: err, Interactive: interactive}
}
func (p *Presenter) Corn() bool                { return p.Theme == "corn" }
func (p *Presenter) SetLiveSink(sink LiveSink) { p.live = sink }
func (p *Presenter) choose(clean, corn string) string {
	if p.Corn() {
		return corn
	}
	return clean
}
func (p *Presenter) line(w io.Writer, kind, clean, corn string, args ...any) {
	p.lineMu.Lock()
	defer p.lineMu.Unlock()
	prefix := map[string]string{"info": "INFO", "ok": "OK", "warn": "WARN", "error": "ERROR"}[kind]
	if p.Corn() {
		prefix = map[string]string{"info": "🌽", "ok": "🌱", "warn": "⚠ CORN", "error": "💥 CORN"}[kind]
	}
	if p.Interactive {
		color := map[string]text.Color{"info": text.FgCyan, "ok": text.FgGreen, "warn": text.FgYellow, "error": text.FgRed}[kind]
		prefix = color.Sprintf("[%s]", prefix)
	} else {
		prefix = "[" + prefix + "]"
	}
	fmt.Fprintf(w, "%s %s\n", prefix, fmt.Sprintf(p.choose(clean, corn), args...))
}
func (p *Presenter) Info(clean, corn string, args ...any) {
	p.line(p.Out, "info", clean, corn, args...)
}
func (p *Presenter) Success(clean, corn string, args ...any) {
	p.line(p.Out, "ok", clean, corn, args...)
}
func (p *Presenter) Warning(clean, corn string, args ...any) {
	p.line(p.Err, "warn", clean, corn, args...)
}
func (p *Presenter) Error(clean, corn string, args ...any) {
	p.line(p.Err, "error", clean, corn, args...)
}

type Row struct{ Label, Value string }

func (p *Presenter) Summary(cleanTitle, cornTitle string, rows ...Row) {
	tw := table.NewWriter()
	tw.SetOutputMirror(p.Out)
	tw.SetTitle(p.choose(cleanTitle, cornTitle))
	for _, r := range rows {
		tw.AppendRow(table.Row{r.Label, r.Value})
	}
	if p.Interactive {
		if p.Corn() {
			tw.SetStyle(table.StyleColoredYellowWhiteOnBlack)
		} else {
			tw.SetStyle(table.StyleRounded)
		}
	} else {
		tw.SetStyle(table.StyleLight)
	}
	tw.Render()
}

type Conversion struct {
	p        *Presenter
	writer   progress.Writer
	trackers map[string]*progress.Tracker
	last     map[string]int64
	liveKeys map[string]bool
	mu       sync.Mutex
	rendered bool
}

type Metadata struct {
	p        *Presenter
	writer   progress.Writer
	trackers map[string]*progress.Tracker
	liveKeys map[string]bool
	mu       sync.Mutex
}

func (p *Presenter) NewMetadata() *Metadata {
	return &Metadata{p: p, trackers: map[string]*progress.Tracker{}, liveKeys: map[string]bool{}}
}

func (m *Metadata) Update(provider, gameID, title, status string, done, total int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := "metadata:" + provider + ":" + gameID
	m.liveKeys[key] = true
	if m.p.live != nil {
		m.p.live.SetLiveJob(key, provider+" · "+title+" · "+status, int64(done), int64(total), status == "querying")
	}
	if !m.p.Interactive {
		m.p.Info("Metadata %d/%d · %s · %s · %s", "Metadata %d/%d · %s · %s · %s", done, total, provider, title, status)
		return
	}
	if m.writer == nil {
		m.writer = progress.NewWriter()
		m.writer.SetOutputWriter(m.p.Out)
		m.writer.SetAutoStop(false)
		if m.p.Corn() {
			m.writer.SetStyle(progress.StyleCircle)
		} else {
			m.writer.SetStyle(progress.StyleBlocks)
		}
		go m.writer.Render()
	}
	trackerKey := provider + "\x00" + gameID
	t := m.trackers[trackerKey]
	if t == nil {
		t = &progress.Tracker{Message: provider + " · " + title, Total: 1}
		m.trackers[trackerKey] = t
		m.writer.AppendTracker(t)
	}
	t.UpdateMessage(provider + " · " + title + " · " + status)
	if status != "querying" {
		t.SetValue(1)
	}
}

func (m *Metadata) Stop(failed bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.p.live != nil {
		for key := range m.liveKeys {
			m.p.live.SetLiveJob(key, "", 0, 0, false)
		}
	}
	if m.writer == nil {
		return
	}
	if failed {
		for _, t := range m.trackers {
			if !t.IsDone() {
				t.MarkAsErrored()
			}
		}
	}
	m.writer.Stop()
}

func (p *Presenter) NewConversion() *Conversion {
	return &Conversion{p: p, trackers: map[string]*progress.Tracker{}, last: map[string]int64{}, liveKeys: map[string]bool{}}
}
func (c *Conversion) Update(part string, done, total int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.p.live != nil {
		c.liveKeys["conversion:"+part] = true
		c.p.live.SetLiveJob("conversion:"+part, part, done, total, done < total)
	}
	if !c.p.Interactive {
		if done == total || done-c.last[part] >= 64<<20 {
			c.p.Info("%s %.1f%% (%d/%d bytes)", "Shucking %s %.1f%% (%d/%d kernels)", part, float64(done)*100/float64(total), done, total)
			c.last[part] = done
		}
		return
	}
	if c.writer == nil {
		c.writer = progress.NewWriter()
		c.writer.SetOutputWriter(c.p.Out)
		c.writer.SetAutoStop(false)
		if c.p.Corn() {
			c.writer.SetStyle(progress.StyleCircle)
		} else {
			c.writer.SetStyle(progress.StyleBlocks)
		}
		go c.writer.Render()
		c.rendered = true
	}
	t := c.trackers[part]
	if t == nil {
		label := part
		if c.p.Corn() {
			label = "Shucking " + part
		}
		t = &progress.Tracker{Message: label, Total: total, Units: progress.UnitsBytes}
		c.trackers[part] = t
		c.writer.AppendTracker(t)
	}
	t.UpdateTotal(total)
	t.SetValue(done)
}
func (c *Conversion) Stop(failed bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.p.live != nil {
		for key := range c.liveKeys {
			c.p.live.SetLiveJob(key, "", 0, 0, false)
		}
	}
	if c.writer == nil {
		return
	}
	if failed {
		for _, t := range c.trackers {
			if !t.IsDone() {
				t.MarkAsErrored()
			}
		}
	}
	c.writer.Stop()
}
