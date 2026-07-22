package storage

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCoreAdaptersParseTypedResults(t *testing.T) {
	blocks, err := parseLSBLK(mustFixture(t, "lsblk.json"))
	require.NoError(t, err)
	require.Len(t, blocks.Resources, 3)
	assert.Equal(t, "disk", blocks.Resources[0].Kind)
	assert.Contains(t, blocks.Relations, Relation{From: stableID("disk", "8:0"), To: stableID("partition", "8:1"), Kind: "contains"})

	mounts, err := parseFindmnt(mustFixture(t, "findmnt.json"))
	require.NoError(t, err)
	require.Len(t, mounts.Mounts, 4)
	assert.Equal(t, "server:/export", mounts.Mounts[1].Source)
	assert.True(t, mounts.Mounts[2].ReadOnly)
}

func TestCoreAdaptersUseFixedCommands(t *testing.T) {
	block := newBlockAdapter("/usr/bin/lsblk")
	mount := newMountAdapter("/usr/bin/findmnt")

	assert.True(t, block.Core())
	assert.Equal(t, "block", block.Name())
	assert.Equal(t, []string{"--json", "--bytes", "--output", "NAME,KNAME,PATH,TYPE,MAJ:MIN,PKNAME,SIZE,FSTYPE,FSVER,LABEL,UUID,MOUNTPOINTS,MODEL,SERIAL,ROTA,RM,RO"}, lsblkArgs)
	assert.True(t, mount.Core())
	assert.Equal(t, "mount", mount.Name())
	assert.Equal(t, []string{"--json", "--list", "--bytes", "--output", "TARGET,SOURCE,FSTYPE,OPTIONS,SIZE,USED,AVAIL,USE%,MAJ:MIN"}, findmntArgs)
}

func TestSortRelationsUsesFromToKindTuple(t *testing.T) {
	relations := []Relation{
		{From: "ab", To: "c", Kind: "z"},
		{From: "a", To: "bc", Kind: "z"},
		{From: "a", To: "bc", Kind: "a"},
	}

	sortRelations(relations)

	assert.Equal(t, []Relation{
		{From: "a", To: "bc", Kind: "a"},
		{From: "a", To: "bc", Kind: "z"},
		{From: "ab", To: "c", Kind: "z"},
	}, relations)
}

func TestParseLSBLKRejectsUnknownFields(t *testing.T) {
	_, err := parseLSBLK([]byte(`{"blockdevices":[],"unexpected":true}`))
	assert.Error(t, err)
}

func TestParseLSBLKRejectsMalformedByteCount(t *testing.T) {
	_, err := parseLSBLK([]byte(`{"blockdevices":[{"name":"sda","kname":"sda","path":"/dev/sda","type":"disk","maj:min":"8:0","pkname":null,"size":"not-a-number","fstype":null,"fsver":null,"label":null,"uuid":null,"mountpoints":null,"model":null,"serial":null,"rota":true,"rm":false,"ro":false}]}`))
	assert.Error(t, err)
}

func TestParseLSBLKAcceptsNumericByteCount(t *testing.T) {
	result, err := parseLSBLK([]byte(`{"blockdevices":[{"name":"sda","kname":"sda","path":"/dev/sda","type":"disk","maj:min":"8:0","pkname":null,"size":1,"fstype":null,"fsver":null,"label":null,"uuid":null,"mountpoints":null,"model":null,"serial":null,"rota":true,"rm":false,"ro":false}]}`))
	require.NoError(t, err)
	assert.Equal(t, uint64(1), result.Resources[0].SizeBytes)
}

func TestParseLSBLKRejectsOversizedField(t *testing.T) {
	name := strings.Repeat("a", maxFieldBytes+1)
	input := []byte(`{"blockdevices":[{"name":"` + name + `","kname":"sda","path":"/dev/sda","type":"disk","maj:min":"8:0","pkname":null,"size":"1","fstype":null,"fsver":null,"label":null,"uuid":null,"mountpoints":null,"model":null,"serial":null,"rota":true,"rm":false,"ro":false}]}`)
	_, err := parseLSBLK(input)
	assert.Error(t, err)
}

func TestParseLSBLKRejectsTooManyResources(t *testing.T) {
	device := `{"name":"sda","kname":"sda","path":"/dev/sda","type":"disk","maj:min":"8:0","pkname":null,"size":"1","fstype":null,"fsver":null,"label":null,"uuid":null,"mountpoints":null,"model":null,"serial":null,"rota":true,"rm":false,"ro":false}`
	_, err := parseLSBLK([]byte(`{"blockdevices":[` + strings.TrimSuffix(strings.Repeat(device+",", maxResources+1), ",") + `]}`))
	assert.Error(t, err)
}

func TestParseFindmntRejectsUnknownFields(t *testing.T) {
	_, err := parseFindmnt([]byte(`{"filesystems":[],"unexpected":true}`))
	assert.Error(t, err)
}

func TestParseFindmntAcceptsFlatLiveShapedRecord(t *testing.T) {
	result, err := parseFindmnt(mustFixture(t, "findmnt-list.json"))
	require.NoError(t, err)
	require.Len(t, result.Mounts, 3)
	pseudo := result.Mounts[2]
	assert.Equal(t, "sysfs", pseudo.Filesystem)
	assert.Equal(t, "/sys", pseudo.Target)
	assert.Equal(t, uint64(0), pseudo.TotalBytes)
	assert.Equal(t, 0.0, pseudo.UsedPercent)
}

func TestParseFindmntRejectsPartialCapacityPlaceholders(t *testing.T) {
	_, err := parseFindmnt([]byte(`{"filesystems":[{"target":"/sys","source":"sysfs","fstype":"sysfs","options":"rw","size":1,"used":0,"avail":0,"use%":"-","maj:min":"0:24"}]}`))
	assert.Error(t, err)
}

func TestParseFindmntRejectsMalformedByteCount(t *testing.T) {
	_, err := parseFindmnt([]byte(`{"filesystems":[{"target":"/","source":"/dev/sda1","fstype":"ext4","options":"rw","size":"1","used":"invalid","avail":"0","use%":"0%","maj:min":"8:1"}]}`))
	assert.Error(t, err)
}

func TestParseFindmntAcceptsNumericByteCounts(t *testing.T) {
	result, err := parseFindmnt([]byte(`{"filesystems":[{"target":"/","source":"/dev/sda1","fstype":"ext4","options":"rw","size":3,"used":2,"avail":1,"use%":"67%","maj:min":"8:1"}]}`))
	require.NoError(t, err)
	assert.Equal(t, uint64(3), result.Mounts[0].TotalBytes)
}

func TestParseFindmntRejectsOversizedField(t *testing.T) {
	target := strings.Repeat("a", maxFieldBytes+1)
	_, err := parseFindmnt([]byte(`{"filesystems":[{"target":"` + target + `","source":"/dev/sda1","fstype":"ext4","options":"rw","size":"1","used":"0","avail":"1","use%":"0%","maj:min":"8:1"}]}`))
	assert.Error(t, err)
}

func TestParseFindmntRejectsTooManyMounts(t *testing.T) {
	mount := `{"target":"/","source":"/dev/sda1","fstype":"ext4","options":"rw","size":"1","used":"0","avail":"1","use%":"0%","maj:min":"8:1"}`
	_, err := parseFindmnt([]byte(`{"filesystems":[` + strings.TrimSuffix(strings.Repeat(mount+",", maxMounts+1), ",") + `]}`))
	assert.Error(t, err)
}

func mustFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	return bytes.TrimSpace(data)
}
