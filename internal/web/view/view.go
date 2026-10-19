package view

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/a-h/templ"

	"github.com/gosuda/rukie/internal/web/negotiate"
	"github.com/gosuda/rukie/internal/web/secheaders"
	"github.com/gosuda/rukie/internal/web/shell"
	"github.com/gosuda/rukie/internal/web/vm"
)

var ErrRefreshTooFast = errors.New("view: refresh below 15s is allowed only on declared live surfaces")

type PageCtx struct {
	RequestID string
	Path      string
	CSRF      string
	Build     string
	Refresh   int
}

func (c PageCtx) shellPage(title string) (shell.Page, error) {
	if c.Refresh > 0 && c.Refresh < 15 {
		return shell.Page{}, ErrRefreshTooFast
	}
	return shell.Page{Title: title, CSRF: c.CSRF, Build: c.Build, Refresh: c.Refresh}, nil
}

func render(h http.Header, w io.Writer, p shell.Page, content templ.Component) error {
	h.Set("Content-Type", "text/html; charset=utf-8")
	secheaders.ApplyHTML(h)
	negotiate.VaryAccept(h)
	return shell.Layout(p, content).Render(context.Background(), w)
}

func WriteErrorPage(h http.Header, w io.Writer, c PageCtx, prob vm.Problem) error {
	p, err := c.shellPage(prob.Title)
	if err != nil {
		return err
	}
	return render(h, w, p, ErrorContent(prob))
}

func WriteAPIIndex(h http.Header, w io.Writer, c PageCtx, idx vm.APIIndex) error {
	p, err := c.shellPage("API operations")
	if err != nil {
		return err
	}
	return render(h, w, p, APIIndexContent(idx))
}

func WriteProblemPage(h http.Header, w io.Writer, c PageCtx, prob vm.Problem) error {
	p, err := c.shellPage(prob.Title)
	if err != nil {
		return err
	}
	return render(h, w, p, ProblemContent(prob, c.RequestID))
}

type Route struct {
	Render func(h http.Header, w io.Writer) error
	Method string
	Path   string
	Status int
}

func sampleCtx() PageCtx {
	return PageCtx{RequestID: "req-sample-01", Path: "/", CSRF: "csrf-sample", Build: "dev"}
}

func sampleProblem() vm.Problem {
	return vm.Problem{
		Status:  500,
		Type:    "internal-error",
		Title:   "Something went wrong",
		Detail:  "The daemon did not answer before the deadline. Retry the operation.",
		TraceID: "req-sample-01",
		Recovery: vm.Recovery{
			BackoffMs: 1000,
			Docs:      "/-/api",
		},
	}
}

func sampleOps() vm.APIIndex {
	return vm.NewAPIIndex([]vm.OpEntry{
		{Name: "repo.get", Method: "GET", Path: "/{owner}/{repo}", Summary: "Read one repository."},
		{Name: "repo.create", Method: "POST", Path: "/new/repo", Summary: "Create a repository."},
		{Name: "change.list", Method: "GET", Path: "/{owner}/{repo}/changes", Summary: "List changes under review."},
	})
}

func Routes() []Route {
	return []Route{
		{
			Method: http.MethodGet,
			Path:   "/-/api",
			Status: http.StatusOK,
			Render: func(h http.Header, w io.Writer) error {
				return WriteAPIIndex(h, w, sampleCtx(), sampleOps())
			},
		},
		{
			Method: http.MethodGet,
			Path:   "/-/problems/internal-error",
			Status: http.StatusInternalServerError,
			Render: func(h http.Header, w io.Writer) error {
				return WriteProblemPage(h, w, sampleCtx(), sampleProblem())
			},
		},
		{
			Method: http.MethodGet,
			Path:   "/error",
			Status: http.StatusInternalServerError,
			Render: func(h http.Header, w io.Writer) error {
				return WriteErrorPage(h, w, sampleCtx(), sampleProblem())
			},
		},
	}
}
