package storage

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"path"
	"strings"
	"unicode"
)

const ManifestFormatVersion = 1

var (
	errInvalidDefinitionID = errors.New("invalid definition ID")
	errInvalidEntropy      = errors.New("invalid definition ID entropy")
	errInvalidNFSHost      = errors.New("invalid NFS host")
	errInvalidNFSExport    = errors.New("invalid NFS export")
	errInvalidNFSVersion   = errors.New("invalid NFS version")
	errInvalidPassword     = errors.New("invalid password")
	errInvalidReadOnly     = errors.New("invalid read-only value")
	errInvalidSMBShare     = errors.New("invalid SMB share")
	errInvalidSMBVersion   = errors.New("invalid SMB version")
	errInvalidTarget       = errors.New("invalid target")
	errInvalidUsername     = errors.New("invalid username")
)

type RemoteManager interface {
	Manager
	Create(context.Context, CreateRequest) error
	Delete(context.Context, string) error
	Mount(context.Context, string) error
	Unmount(context.Context, string) error
}

type CreateRequest struct {
	Export   string
	Host     string
	ID       string
	Password string
	Protocol string
	ReadOnly bool
	Server   string
	Share    string
	Target   string
	Username string
	Version  string
}

type Definition struct {
	CreatedTarget   bool   `json:"created_target"`
	Credential      string `json:"credential,omitempty"`
	Export          string `json:"export,omitempty"`
	FormatVersion   int    `json:"format_version"`
	Host            string `json:"host,omitempty"`
	ID              string `json:"id"`
	Protocol        string `json:"protocol"`
	ProtocolVersion string `json:"protocol_version"`
	ReadOnly        bool   `json:"read_only"`
	Server          string `json:"server,omitempty"`
	Share           string `json:"share,omitempty"`
	State           string `json:"state"`
	Target          string `json:"target"`
	UnitName        string `json:"unit_name"`
	Username        string `json:"username,omitempty"`
}

func NewDefinitionID(random io.Reader) (string, error) {
	bytes := make([]byte, 16)
	if _, err := io.ReadFull(random, bytes); err != nil {
		return "", errInvalidEntropy
	}
	return hex.EncodeToString(bytes), nil
}

func ValidateDefinitionID(value string) error {
	if len(value) != 32 {
		return errInvalidDefinitionID
	}
	for _, r := range value {
		if ('0' > r || r > '9') && ('a' > r || r > 'f') {
			return errInvalidDefinitionID
		}
	}
	return nil
}

func ValidateNFSHost(value string) error {
	if net.ParseIP(value) != nil || validDNSName(value) {
		return nil
	}
	return errInvalidNFSHost
}

func ValidateNFSExport(value string) error {
	if !path.IsAbs(value) || strings.Contains(value, ",") || hasControl(value) {
		return errInvalidNFSExport
	}
	return nil
}

func ValidateNFSVersion(value string) error {
	if value == "auto" || value == "3" || value == "4" || value == "4.1" || value == "4.2" {
		return nil
	}
	return errInvalidNFSVersion
}

func ValidatePassword(value string) error {
	if len(value) == 0 || len(value) > 512 || strings.ContainsAny(value, "\x00\r\n") {
		return errInvalidPassword
	}
	return nil
}

func ParseReadOnly(value string) (bool, error) {
	switch value {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, errInvalidReadOnly
	}
}

func ValidateSMBShare(value string) error {
	if value == "" || strings.ContainsAny(value, "/\\") || hasControl(value) {
		return errInvalidSMBShare
	}
	return nil
}

func ValidateSMBVersion(value string) error {
	if value == "auto" || value == "2.1" || value == "3.0" || value == "3.1.1" {
		return nil
	}
	return errInvalidSMBVersion
}

func ValidateTarget(value string) error {
	if !path.IsAbs(value) || path.Clean(value) != value || hasControl(value) {
		return errInvalidTarget
	}
	return nil
}

func ValidateUsername(value string) error {
	if len(value) == 0 || len(value) > 256 || hasControl(value) {
		return errInvalidUsername
	}
	return nil
}

func hasControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}

func validDNSName(value string) bool {
	if len(value) == 0 || len(value) > 253 {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if ('a' > r || r > 'z') && ('A' > r || r > 'Z') && ('0' > r || r > '9') && r != '-' {
				return false
			}
		}
	}
	return true
}
