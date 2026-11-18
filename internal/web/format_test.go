package web

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		name  string
		value uint64
		want  string
	}{
		{name: "zero", value: 0, want: "0 B"},
		{name: "one byte", value: 1, want: "1 B"},
		{name: "512 bytes", value: 512, want: "512 B"},
		{name: "below kibibyte", value: 1023, want: "1023 B"},
		{name: "kibibyte", value: 1024, want: "1.0 KiB"},
		{name: "fractional kibibyte", value: 1536, want: "1.5 KiB"},
		{name: "below mebibyte", value: 1048575, want: "1024.0 KiB"},
		{name: "mebibyte", value: 1048576, want: "1.0 MiB"},
		{name: "fractional gibibyte", value: 1610612736, want: "1.5 GiB"},
		{name: "tebibyte", value: 1 << 40, want: "1.0 TiB"},
		{name: "pebibyte", value: 1 << 50, want: "1.0 PiB"},
		{name: "exbibyte", value: 1 << 60, want: "1.0 EiB"},
		{name: "maximum", value: math.MaxUint64, want: "16.0 EiB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, FormatBytes(tt.value))
		})
	}
}
