package sysext

import "context"

// Manager is the mutating extensions seam: the four lifecycle operations
// cmd/pilothoused's registerSysextActions registers behind
// broker.ActionSysext{Disable,Enable,Refresh,Update}, each under its own
// capability guard. It is deliberately separate from ExtensionsSource so the
// read path cannot reach a mutation, and it carries no read method of its own
// -- the aggregate inventory is ExtensionsSource.State's job.
//
// Like ExtensionsSource, the interface lives in this package while its only
// production implementation lives in the extctl subpackage; nothing the web
// binary links can invoke a host tool.
type Manager interface {
	Disable(context.Context, string) error
	Enable(context.Context, string) error
	Refresh(context.Context) error
	Update(context.Context) error
}
