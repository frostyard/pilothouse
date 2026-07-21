//go:build !sdjournal

package journal

func New() Reader {
	return Reader{open: func() (source, error) { return nil, errUnavailable }}
}
