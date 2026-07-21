//go:build sdjournal

package journal

import (
	"errors"
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecordFieldsTreatsAbsentJournalFieldsAsAbsentData(t *testing.T) {
	fields, err := recordFields(func(name string) (string, error) {
		switch name {
		case "PRIORITY", "MESSAGE":
			return name + "=value", nil
		default:
			return "", syscall.ENOENT
		}
	})

	require.NoError(t, err)
	assert.Equal(t, map[string]string{"PRIORITY": "value", "MESSAGE": "value"}, fields)
}

func TestRecordFieldsRejectsMalformedFramingAndAPIErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		get  func(string) (string, error)
	}{
		{"malformed framing", func(string) (string, error) { return "MESSAGE", nil }},
		{"wrong field", func(string) (string, error) { return "OTHER=value", nil }},
		{"api error", func(string) (string, error) { return "", errors.New("read failed") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := recordFields(tc.get)
			assert.Error(t, err)
		})
	}
}

func TestJournalSourceTailSmoke(t *testing.T) {
	if os.Getenv("JOURNAL_SMOKE") != "1" {
		t.Skip("set JOURNAL_SMOKE=1 to read an accessible live system journal")
	}

	reader := New()
	source, err := reader.open()
	require.NoError(t, err)
	defer func() { require.NoError(t, source.Close()) }()
	require.NoError(t, source.SeekTail())
	next, err := source.Previous()
	require.NoError(t, err)
	require.NotZero(t, next, "an accessible live journal must have a newest entry")
	record, err := source.Record()
	require.NoError(t, err)
	assert.False(t, record.Timestamp.IsZero())
}
