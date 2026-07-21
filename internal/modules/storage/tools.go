package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
)

var errOutputTooLarge = errors.New("tool output exceeds limit")

type fileIdentity struct {
	Mode    os.FileMode
	Regular bool
	UID     uint32
}

type commandRunner struct {
	limit int
	run   func(context.Context, string, ...string) ([]byte, error)
}

func (r commandRunner) Run(ctx context.Context, path string, args ...string) ([]byte, error) {
	if r.run != nil {
		output, err := r.run(ctx, path, args...)
		if len(output) > r.limit {
			return nil, errOutputTooLarge
		}
		return output, err
	}

	stdout := boundedBuffer{limit: r.limit}
	stderr := boundedBuffer{limit: r.limit}
	command := exec.CommandContext(ctx, path, args...)
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if errors.Is(stdout.err, errOutputTooLarge) || errors.Is(stderr.err, errOutputTooLarge) {
		return nil, errOutputTooLarge
	}
	if err != nil {
		return nil, fmt.Errorf("run %s: %w", path, err)
	}
	return stdout.data, nil
}

type boundedBuffer struct {
	data  []byte
	err   error
	limit int
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	remaining := b.limit - len(b.data)
	if remaining <= 0 {
		b.err = errOutputTooLarge
		return 0, errOutputTooLarge
	}
	if len(data) > remaining {
		b.data = append(b.data, data[:remaining]...)
		b.err = errOutputTooLarge
		return remaining, errOutputTooLarge
	}
	b.data = append(b.data, data...)
	return len(data), nil
}

var _ io.Writer = (*boundedBuffer)(nil)

type Toolset struct {
	Findmnt string
	LSBLK   string
}

func NewToolset() (Toolset, error) {
	lsblk, err := resolveSystemTool("lsblk", []string{"/usr/bin/lsblk", "/bin/lsblk"})
	if err != nil {
		return Toolset{}, err
	}
	findmnt, err := resolveSystemTool("findmnt", []string{"/usr/bin/findmnt", "/bin/findmnt"})
	if err != nil {
		return Toolset{}, err
	}
	return Toolset{LSBLK: lsblk, Findmnt: findmnt}, nil
}

func resolveSystemTool(name string, candidates []string) (string, error) {
	return resolveTool(candidates, func(path string) (fileIdentity, error) {
		info, err := os.Lstat(path)
		if err != nil {
			return fileIdentity{}, err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fileIdentity{}, fmt.Errorf("inspect %s ownership", path)
		}
		return fileIdentity{Mode: info.Mode(), Regular: info.Mode().IsRegular(), UID: stat.Uid}, nil
	})
}

func resolveTool(candidates []string, identity func(string) (fileIdentity, error)) (string, error) {
	var lastErr error
	for _, path := range candidates {
		file, err := identity(path)
		if err != nil {
			lastErr = err
			continue
		}
		if !file.Regular {
			lastErr = fmt.Errorf("tool %s is not a regular file", path)
			continue
		}
		if file.UID != 0 {
			lastErr = fmt.Errorf("tool %s is not root-owned", path)
			continue
		}
		if file.Mode.Perm()&0o022 != 0 {
			lastErr = fmt.Errorf("tool %s is group- or world-writable", path)
			continue
		}
		return path, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no tool candidates")
	}
	return "", fmt.Errorf("resolve tool: %w", lastErr)
}

func resolveOptionalTool(candidates []string, lstat func(string) (os.FileInfo, error)) (string, bool, error) {
	return ResolveOptionalTool(candidates, lstat)
}

func ResolveOptionalTool(candidates []string, lstat func(string) (os.FileInfo, error)) (string, bool, error) {
	for _, path := range candidates {
		info, err := lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", false, fmt.Errorf("inspect optional tool %s: %w", path, err)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return "", false, fmt.Errorf("optional tool %s is not a regular file", path)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return "", false, fmt.Errorf("inspect %s ownership", path)
		}
		if stat.Uid != 0 {
			return "", false, fmt.Errorf("optional tool %s is not root-owned", path)
		}
		if info.Mode().Perm()&0o022 != 0 {
			return "", false, fmt.Errorf("optional tool %s is group- or world-writable", path)
		}
		return path, true, nil
	}
	return "", false, nil
}
