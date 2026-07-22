package storage

import (
	"errors"
	"strings"
	"testing"

	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRemoteBrokerIDs(t *testing.T) {
	assert.Equal(t, "org.frostyard.pilothouse.storage.create-nfs", broker.ActionStorageCreateNFS)
	assert.Equal(t, "org.frostyard.pilothouse.storage.create-smb-guest", broker.ActionStorageCreateSMBGuest)
	assert.Equal(t, "org.frostyard.pilothouse.storage.create-smb-credentials", broker.ActionStorageCreateSMBCredentials)
	assert.Equal(t, "org.frostyard.pilothouse.storage.mount", broker.ActionStorageMount)
	assert.Equal(t, "org.frostyard.pilothouse.storage.unmount", broker.ActionStorageUnmount)
	assert.Equal(t, "org.frostyard.pilothouse.storage.delete", broker.ActionStorageDelete)
}

func TestValidateNFSHost(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		valid bool
	}{
		{"dns", "nas.example", true},
		{"ipv4", "192.0.2.1", true},
		{"ipv6", "2001:db8::1", true},
		{"underscore", "nas_server.example", false},
		{"traversal", "../nas", false},
		{"control", "nas\x00.example", false},
		{"newline", "nas\n.example", false},
		{"empty", "", false},
		{"too long", strings.Repeat("a", 513), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertValidation(t, ValidateNFSHost(test.value), test.valid, test.value)
		})
	}
}

func TestValidateNFSExport(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		valid bool
	}{
		{"absolute", "/exports/media", true},
		{"relative", "exports/media", false},
		{"comma", "/exports,media", false},
		{"control", "/exports/\x00media", false},
		{"newline", "/exports/\nmedia", false},
		{"empty", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertValidation(t, ValidateNFSExport(test.value), test.valid, test.value)
		})
	}
}

func TestValidateSMBShare(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		valid bool
	}{
		{"simple", "media", true},
		{"space", "media archive", true},
		{"slash", "media/archive", false},
		{"backslash", "media\\archive", false},
		{"control", "media\x00archive", false},
		{"newline", "media\narchive", false},
		{"empty", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertValidation(t, ValidateSMBShare(test.value), test.valid, test.value)
		})
	}
}

func TestValidateVersions(t *testing.T) {
	for _, test := range []struct {
		name     string
		validate func(string) error
		value    string
		valid    bool
	}{
		{"nfs auto", ValidateNFSVersion, "auto", true},
		{"nfs 3", ValidateNFSVersion, "3", true},
		{"nfs 4", ValidateNFSVersion, "4", true},
		{"nfs 4.1", ValidateNFSVersion, "4.1", true},
		{"nfs 4.2", ValidateNFSVersion, "4.2", true},
		{"nfs invalid", ValidateNFSVersion, "4.0", false},
		{"smb auto", ValidateSMBVersion, "auto", true},
		{"smb 2.1", ValidateSMBVersion, "2.1", true},
		{"smb 3.0", ValidateSMBVersion, "3.0", true},
		{"smb 3.1.1", ValidateSMBVersion, "3.1.1", true},
		{"smb invalid", ValidateSMBVersion, "3.1", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertValidation(t, test.validate(test.value), test.valid, test.value)
		})
	}
}

func TestParseReadOnly(t *testing.T) {
	for _, test := range []struct {
		value string
		want  bool
		valid bool
	}{
		{"true", true, true},
		{"false", false, true},
		{"TRUE", false, false},
		{"1", false, false},
		{"", false, false},
	} {
		t.Run(test.value, func(t *testing.T) {
			got, err := ParseReadOnly(test.value)
			assertValidation(t, err, test.valid, test.value)
			if test.valid {
				assert.Equal(t, test.want, got)
			}
		})
	}
}

func TestValidateDefinitionID(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		valid bool
	}{
		{"opaque id", "0123456789abcdef0123456789abcdef", true},
		{"uppercase", "0123456789ABCDEF0123456789ABCDEF", false},
		{"short", "0123456789abcdef", false},
		{"newline", "0123456789abcdef0123456789abcde\n", false},
		{"empty", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertValidation(t, ValidateDefinitionID(test.value), test.valid, test.value)
		})
	}
}

func TestNewDefinitionID(t *testing.T) {
	id, err := NewDefinitionID(strings.NewReader("0123456789abcdef"))
	require.NoError(t, err)
	assert.Equal(t, "30313233343536373839616263646566", id)
	require.NoError(t, ValidateDefinitionID(id))

	_, err = NewDefinitionID(strings.NewReader("too short"))
	assert.Error(t, err)
}

func TestValidateUsername(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		valid bool
	}{
		{"valid", "mount-user", true},
		{"empty", "", false},
		{"control", "mount\x00user", false},
		{"newline", "mount\nuser", false},
		{"too long", strings.Repeat("a", 257), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertValidation(t, ValidateUsername(test.value), test.valid, test.value)
		})
	}
}

func TestValidatePassword(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		valid bool
	}{
		{"valid", "correct horse battery staple", true},
		{"empty", "", false},
		{"nul", "password\x00", false},
		{"carriage return", "password\r", false},
		{"newline", "password\n", false},
		{"too long", strings.Repeat("a", 513), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertValidation(t, ValidatePassword(test.value), test.valid, test.value)
		})
	}
}

func TestValidateTarget(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		valid bool
	}{
		{"absolute clean", "/mnt/media", true},
		{"root", "/", true},
		{"relative", "mnt/media", false},
		{"traversal", "/mnt/../media", false},
		{"repeated separator", "/mnt//media", false},
		{"control", "/mnt/\x00media", false},
		{"newline", "/mnt/\nmedia", false},
		{"empty", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertValidation(t, ValidateTarget(test.value), test.valid, test.value)
		})
	}
}

func assertValidation(t *testing.T, err error, valid bool, submitted string) {
	t.Helper()
	if valid {
		assert.NoError(t, err)
		return
	}
	require.Error(t, err)
	if submitted != "" {
		assert.NotContains(t, err.Error(), submitted)
	}
}

func TestNewDefinitionIDReturnsStableError(t *testing.T) {
	_, err := NewDefinitionID(errorReader{})
	assert.EqualError(t, err, "invalid definition ID entropy")
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("unavailable") }
