package cli

import (
	"bytes"
	"testing"

	"github.com/m1r3dk/dirhop/internal/model"
)

func treeFixture() (model.Entry, []model.Entry) {
	rootID, alphaID, nestedID := int64(1), int64(2), int64(6)
	size10, size20, size30, size40 := int64(10), int64(20), int64(30), int64(40)
	root := model.Entry{ID: rootID, Name: "/", NormalizedPath: "/", Type: model.EntryTypeDirectory}
	return root, []model.Entry{
		// Deliberately shuffled: renderer, not database row order, owns output order.
		{ID: 8, ParentID: &nestedID, Name: "deep.txt", NormalizedPath: "/alpha/nested/deep.txt", Type: model.EntryTypeFile, Size: &size40},
		{ID: 4, ParentID: &rootID, Name: "root.txt", NormalizedPath: "/root.txt", Type: model.EntryTypeFile, Size: &size10},
		root,
		{ID: 7, ParentID: &alphaID, Name: "z.txt", NormalizedPath: "/alpha/z.txt", Type: model.EntryTypeFile, Size: &size30},
		{ID: 3, ParentID: &rootID, Name: "beta", NormalizedPath: "/beta", Type: model.EntryTypeDirectory},
		{ID: alphaID, ParentID: &rootID, Name: "alpha", NormalizedPath: "/alpha", Type: model.EntryTypeDirectory},
		{ID: 5, ParentID: &alphaID, Name: "a.txt", NormalizedPath: "/alpha/a.txt", Type: model.EntryTypeFile, Size: &size20},
		{ID: nestedID, ParentID: &alphaID, Name: "nested", NormalizedPath: "/alpha/nested", Type: model.EntryTypeDirectory},
	}
}

func TestPrintTreeMatchesStandardTreeLayout(t *testing.T) {
	root, entries := treeFixture()
	var out bytes.Buffer
	if err := printTree(&out, ".", root, entries, treeOptions{}); err != nil {
		t.Fatal(err)
	}
	want := ".\n" +
		"├── alpha\n" +
		"│   ├── a.txt\n" +
		"│   ├── nested\n" +
		"│   │   └── deep.txt\n" +
		"│   └── z.txt\n" +
		"├── beta\n" +
		"└── root.txt\n" +
		"\n" +
		"4 directories, 4 files\n"
	if got := out.String(); got != want {
		t.Fatalf("tree output:\n%s\nwant:\n%s", got, want)
	}
}

func TestPrintTreeDepthAndDirectoriesOnly(t *testing.T) {
	root, entries := treeFixture()
	var out bytes.Buffer
	if err := printTree(&out, "/", root, entries, treeOptions{depth: 2, dirsOnly: true}); err != nil {
		t.Fatal(err)
	}
	want := "/\n" +
		"├── alpha\n" +
		"│   └── nested\n" +
		"└── beta\n" +
		"\n" +
		"4 directories\n"
	if got := out.String(); got != want {
		t.Fatalf("tree -L 2 -d output:\n%s\nwant:\n%s", got, want)
	}
}

func TestPrintTreeDepthSummaryCountsOnlyVisibleEntries(t *testing.T) {
	root, entries := treeFixture()
	var out bytes.Buffer
	if err := printTree(&out, ".", root, entries, treeOptions{depth: 1}); err != nil {
		t.Fatal(err)
	}
	want := ".\n" +
		"├── alpha\n" +
		"├── beta\n" +
		"└── root.txt\n" +
		"\n" +
		"3 directories, 1 file\n"
	if got := out.String(); got != want {
		t.Fatalf("tree -L 1 output:\n%s\nwant:\n%s", got, want)
	}
}

func TestPrintTreeFilesOnlyUsesUnambiguousRelativePaths(t *testing.T) {
	root, entries := treeFixture()
	var out bytes.Buffer
	if err := printTree(&out, ".", root, entries, treeOptions{filesOnly: true}); err != nil {
		t.Fatal(err)
	}
	want := ".\n" +
		"├── alpha/a.txt\n" +
		"├── alpha/nested/deep.txt\n" +
		"├── alpha/z.txt\n" +
		"└── root.txt\n" +
		"\n" +
		"0 directories, 4 files\n"
	if got := out.String(); got != want {
		t.Fatalf("tree -f output:\n%s\nwant:\n%s", got, want)
	}
}

func TestPrintTreeSizesUseStandardAlignedByteColumn(t *testing.T) {
	root, entries := treeFixture()
	var out bytes.Buffer
	if err := printTree(&out, ".", root, entries, treeOptions{depth: 1, sizes: true}); err != nil {
		t.Fatal(err)
	}
	want := ".\n" +
		"├── alpha\n" +
		"├── beta\n" +
		"└── [         10]  root.txt\n" +
		"\n" +
		"3 directories, 1 file\n"
	if got := out.String(); got != want {
		t.Fatalf("tree --sizes output:\n%s\nwant:\n%s", got, want)
	}
}
