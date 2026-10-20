package access

import (
	"context"

	"github.com/gosuda/rukie/internal/op"
)

func Allow() op.Guard {
	return func(_ context.Context, _ op.Principal, _ op.Params) error {
		return nil
	}
}

func RequireScope(s op.Scope) op.Guard {
	return func(_ context.Context, p op.Principal, _ op.Params) error {
		if p.Can(s) {
			return nil
		}
		return op.Errorf(op.CodeForbidden, "missing scope %q", s.String())
	}
}
