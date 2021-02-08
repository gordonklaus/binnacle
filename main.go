package main

import (
	"context"
	"flag"
	"image/color"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"gioui.org/app"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"gioui.org/font/gofont"
	"github.com/prometheus/client_golang/api"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/config"
)

func main() {
	promURL := flag.String("addr", "", "fully-qualified URL of prometheus instance")
	flag.Parse()
	client, err := api.NewClient(api.Config{
		Address:      *promURL,
		RoundTripper: config.NewBearerAuthRoundTripper(config.Secret(os.Getenv("PROM_TOKEN")), api.DefaultRoundTripper),
	})
	if err != nil {
		log.Fatal("Could not configure prom client", err)
	}

	go func() {
		w := app.NewWindow(app.Title("Binnacle"))
		if err := loop(w, client); err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
	}()
	app.Main()
}

type Backend struct {
	v1.API

	// protects cancel func
	sync.Mutex
	cancel func()

	Timeout time.Duration
	Updates chan queryResult
}

func NewBackend(client api.Client) *Backend {
	return &Backend{
		API:     v1.NewAPI(client),
		Updates: make(chan queryResult),
		Timeout: time.Second * 10,
	}
}
func (b *Backend) Query(text string) {
	b.Lock()
	// cancel any previously executing query
	if b.cancel != nil {
		b.cancel()
	}
	// create a new context
	ctx, cancel := context.WithTimeout(context.Background(), b.Timeout)
	// configure future queries to cancel this context
	b.cancel = cancel
	b.Unlock()
	defer cancel()
	result, warnings, err := b.API.Query(ctx, text, time.Now())
	var data []string
	if result != nil {
		data = strings.Split(result.String(), "\n")
	}
	b.Updates <- queryResult{
		data:     data,
		warnings: warnings,
		error:    err,
	}
}

type (
	C = layout.Context
	D = layout.Dimensions
)

func format(ed *widget.Editor) {
	start, end := ed.Selection()
	forward := true
	if end < start {
		forward = false
		start, end = end, start
	}
	text := ed.Text()
	before := text[:start]
	selected := text[start:end]
	after := text[end:]

	depth := 0
	for _, slice := range []*string{&before, &selected, &after} {
		var result strings.Builder
		for i, line := range strings.Split(*slice, "\n") {
			var leadingCloseParens int
			newLine := strings.TrimRight(strings.TrimLeft(line, " \t"), "\t\n")
			strings.TrimLeftFunc(newLine, func(r rune) bool {
				if r == ')' {
					leadingCloseParens++
					return true
				}
				return false
			})
			prefix := strings.Repeat("  ", depth-leadingCloseParens)
			depth += strings.Count(line, "(") - strings.Count(line, ")")
			if i > 0 {
				result.Write([]byte("\n"))
				result.Write([]byte(prefix))
			}
			result.Write([]byte(newLine))
		}
		*slice = result.String()
	}

	endStart := len(before)
	endEnd := len(selected) + endStart
	if !forward {
		endStart, endEnd = endEnd, endStart
	}
	finalText := before + selected + after
	if finalText != text {
		ed.SetText(finalText)
		ed.SetCaret(endStart, endEnd)
	}
}

type queryResult struct {
	data     []string
	warnings []string
	error
}

func loop(w *app.Window, client api.Client) error {
	backEnd := NewBackend(client)

	th := material.NewTheme(gofont.Collection())
	var (
		ops          op.Ops
		editor       widget.Editor
		dataList     layout.List
		data         []string
		warnings     []string
		warningsList layout.List
		errorText    string
		inset        = layout.UniformInset(unit.Dp(4))
	)
	dataList.Axis = layout.Vertical
	warningsList.Axis = layout.Vertical
	for {
		select {
		case e := <-w.Events():
			switch e := e.(type) {
			case system.DestroyEvent:
				return e.Err
			case system.FrameEvent:
				gtx := layout.NewContext(&ops, e)
				var editorChanged = false
				for _, e := range editor.Events() {
					switch e.(type) {
					case widget.ChangeEvent:
						editorChanged = true
					}
				}
				if editorChanged {
					format(&editor)
					go backEnd.Query(editor.Text())
				}
				layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						return inset.Layout(gtx, func(gtx C) D {
							return widget.Border{
								Width: unit.Dp(2),
								Color: th.Fg,
							}.Layout(gtx, func(gtx C) D {
								return inset.Layout(gtx, func(gtx C) D {
									gtx.Constraints.Min.X = gtx.Constraints.Max.X
									gtx.Constraints.Min.Y = 0
									ed := material.Editor(th, &editor, "query")
									ed.Font.Variant = "Mono"
									return ed.Layout(gtx)
								})
							})
						})
					}),
					layout.Rigid(func(gtx C) D {
						if len(errorText) == 0 {
							return D{}
						}
						return inset.Layout(gtx, func(gtx C) D {
							label := material.Body1(th, errorText)
							label.Font.Variant = "Mono"
							label.Color = color.NRGBA{R: 0x6e, G: 0x0a, B: 0x1e, A: 255}
							return label.Layout(gtx)
						})
					}),
					layout.Rigid(func(gtx C) D {
						if len(warnings) == 0 {
							return D{}
						}
						return inset.Layout(gtx, func(gtx C) D {
							return warningsList.Layout(gtx, len(warnings), func(gtx C, index int) D {
								label := material.Body1(th, warnings[index])
								label.Font.Variant = "Mono"
								label.Color = color.NRGBA{R: 0xd4, G: 0xaf, B: 0x37, A: 255}
								return label.Layout(gtx)
							})
						})
					}),
					layout.Flexed(1.0, func(gtx C) D {
						return inset.Layout(gtx, func(gtx C) D {
							return dataList.Layout(gtx, len(data), func(gtx C, index int) D {
								label := material.Body1(th, data[index])
								label.Font.Variant = "Mono"
								return label.Layout(gtx)
							})
						})
					}),
				)
				e.Frame(gtx.Ops)
			}
		case result := <-backEnd.Updates:
			if result.error != nil {
				errorText = result.Error()
				warnings = nil
			} else {
				data = result.data
				warnings = result.warnings
				errorText = ""
			}
			w.Invalidate()
		}
	}
}
