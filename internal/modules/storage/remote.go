package storage

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"path"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	LegacyManifestFormatVersion = 1
	ManifestFormatVersion       = 2
)

var (
	errInvalidDefinitionID = errors.New("invalid definition ID")
	errInvalidEntropy      = errors.New("invalid definition ID entropy")
	errInvalidHost         = errors.New("invalid host")
	errInvalidNFSExport    = errors.New("invalid NFS export")
	errInvalidNFSHost      = errors.New("invalid NFS host")
	errInvalidNFSVersion   = errors.New("invalid NFS version")
	errInvalidPassword     = errors.New("invalid password")
	errInvalidProtocol     = errors.New("invalid protocol")
	errInvalidReadOnly     = errors.New("invalid read-only value")
	errInvalidSMBShare     = errors.New("invalid SMB share")
	errInvalidSMBOwnership = errors.New("invalid SMB ownership")
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
	SMBOwnership
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
	SMBOwnership
}

type SMBOwnership struct {
	UID string `json:"uid,omitempty"`
	GID string `json:"gid,omitempty"`
}

func NewDefinitionID(random io.Reader) (string, error) {
	bytes := make([]byte, 16)
	if _, err := io.ReadFull(random, bytes); err != nil {
		return "", errInvalidEntropy
	}
	return hex.EncodeToString(bytes), nil
}

func ValidateDefinitionID(value string) error {
	if !validText(value) || len(value) != 32 {
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
	if err := ValidateHost(value); err != nil {
		return errInvalidNFSHost
	}
	return nil
}

func ValidateSMBServer(value string) error {
	return ValidateHost(value)
}

func ValidateHost(value string) error {
	if !validText(value) {
		return errInvalidHost
	}
	if net.ParseIP(value) != nil || validDNSName(value) {
		return nil
	}
	return errInvalidHost
}

func ValidateNFSExport(value string) error {
	if !validText(value) || !path.IsAbs(value) || strings.Contains(value, ",") || hasControl(value) {
		return errInvalidNFSExport
	}
	return nil
}

func ValidateNFSVersion(value string) error {
	if !validText(value) {
		return errInvalidNFSVersion
	}
	if value == "auto" || value == "3" || value == "4" || value == "4.1" || value == "4.2" {
		return nil
	}
	return errInvalidNFSVersion
}

func ValidatePassword(value string) error {
	if !validText(value) || len(value) == 0 || len(value) > 512 || strings.ContainsAny(value, "\x00\r\n") {
		return errInvalidPassword
	}
	return nil
}

func ValidateProtocol(value string) error {
	if !validText(value) || value != "nfs" && value != "smb" {
		return errInvalidProtocol
	}
	return nil
}

func ParseReadOnly(value string) (bool, error) {
	if !validText(value) {
		return false, errInvalidReadOnly
	}
	switch value {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, errInvalidReadOnly
	}
}

func ParseSMBOwnership(uid, gid string) (SMBOwnership, error) {
	if uid == "" && gid == "" {
		return SMBOwnership{}, nil
	}
	if uid == "" || gid == "" {
		return SMBOwnership{}, errInvalidSMBOwnership
	}
	parse := func(value string) (string, error) {
		if !validText(value) || strings.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
			return "", errInvalidSMBOwnership
		}
		number, err := strconv.ParseUint(value, 10, 32)
		if err != nil || number == uint64(^uint32(0)) {
			return "", errInvalidSMBOwnership
		}
		return strconv.FormatUint(number, 10), nil
	}
	canonicalUID, err := parse(uid)
	if err != nil {
		return SMBOwnership{}, err
	}
	canonicalGID, err := parse(gid)
	if err != nil {
		return SMBOwnership{}, err
	}
	return SMBOwnership{UID: canonicalUID, GID: canonicalGID}, nil
}

func ValidateSMBShare(value string) error {
	if !validText(value) || value == "" || strings.ContainsAny(value, "/\\") || hasControl(value) {
		return errInvalidSMBShare
	}
	return nil
}

func ValidateSMBVersion(value string) error {
	if !validText(value) {
		return errInvalidSMBVersion
	}
	if value == "auto" || value == "2.1" || value == "3.0" || value == "3.1.1" {
		return nil
	}
	return errInvalidSMBVersion
}

func ValidateTarget(value string) error {
	if !validText(value) || !path.IsAbs(value) || path.Clean(value) != value || hasControl(value) {
		return errInvalidTarget
	}
	return nil
}

func ValidateUsername(value string) error {
	if !validText(value) || len(value) == 0 || len(value) > 256 || hasControl(value) {
		return errInvalidUsername
	}
	return nil
}

// nfsMountSource brackets IPv6 literals so the rendered source stays a valid
// host:export mount specification.
func nfsMountSource(host, export string) string {
	if strings.Contains(host, ":") {
		return "[" + host + "]:" + export
	}
	return host + ":" + export
}

func hasControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}

func validText(value string) bool { return utf8.ValidString(value) }

func validateDefinitionOwnership(formatVersion int, protocol string, ownership SMBOwnership) error {
	if formatVersion != LegacyManifestFormatVersion && formatVersion != ManifestFormatVersion {
		return errInvalidSMBOwnership
	}
	if formatVersion == LegacyManifestFormatVersion {
		if ownership != (SMBOwnership{}) {
			return errInvalidSMBOwnership
		}
		return nil
	}
	canonical, err := ParseSMBOwnership(ownership.UID, ownership.GID)
	if err != nil || canonical != ownership || protocol != "smb" && ownership != (SMBOwnership{}) {
		return errInvalidSMBOwnership
	}
	return nil
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
