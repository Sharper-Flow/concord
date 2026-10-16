package store

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"strings"
	"testing"
)

const sliceDiffProjectID = "concord"

var (
	sliceDiffOIDBase = strings.Repeat("1", 40)
	sliceDiffOIDEnd  = strings.Repeat("2", 40)
	sliceDiffOIDZero = strings.Repeat("0", 40)
)

// sliceDiffRawRecord renders one `--raw -z --no-renames` change as Git
// writes it, with the zero object ID on the absent side of an addition or
// deletion. The checker reads hex syntax only; full object identity stays
// with the native consumer.
func sliceDiffRawRecord(srcMode, dstMode, status, path string) string {
	srcOID, dstOID := sliceDiffOIDBase, sliceDiffOIDEnd
	if srcMode == "000000" {
		srcOID = sliceDiffOIDZero
	}
	if dstMode == "000000" {
		dstOID = sliceDiffOIDZero
	}
	return fmt.Sprintf(":%s %s %s %s %s\x00%s\x00", srcMode, dstMode, srcOID, dstOID, status, path)
}

// sliceDiffNumstatRecord renders one `--numstat -z` record:
// "additions\tdeletions\tpath" NUL-terminated.
func sliceDiffNumstatRecord(additions, deletions, path string) string {
	return fmt.Sprintf("%s\t%s\t%s\x00", additions, deletions, path)
}

// sliceDiffTextBlob builds deterministic valid UTF-8 text with one line per
// requested line. The checker validates blob shape only, so the line count
// of a supplied blob never has to agree with the numstat pair.
func sliceDiffTextBlob(lines int) []byte {
	b := make([]byte, 0, lines*8)
	for i := 0; i < lines; i++ {
		b = append(b, fmt.Sprintf("line %d\n", i)...)
	}
	return b
}

func sliceDiffFixture() sliceDiffInput {
	const (
		countedPath = "internal/store/worker_slice_diff.go"
		testPath    = "internal/store/worker_slice_diff_test.go"
		deletedPath = "internal/store/obsolete.go"
	)
	return sliceDiffInput{
		ProjectID: sliceDiffProjectID,
		RawDiff: []byte(strings.Join([]string{
			sliceDiffRawRecord("100644", "100644", "M", countedPath),
			sliceDiffRawRecord("000000", "100644", "A", testPath),
			sliceDiffRawRecord("100644", "000000", "D", deletedPath),
		}, "")),
		Numstat: []byte(strings.Join([]string{
			sliceDiffNumstatRecord("200", "200", countedPath),
			sliceDiffNumstatRecord("120", "0", testPath),
			sliceDiffNumstatRecord("0", "30", deletedPath),
		}, "")),
		OldBlobs: map[string][]byte{
			countedPath: sliceDiffTextBlob(3),
			deletedPath: sliceDiffTextBlob(2),
		},
		NewBlobs: map[string][]byte{
			countedPath: sliceDiffTextBlob(4),
		},
		Files: []WorkflowDesignArchitectureFile{
			{ProjectID: sliceDiffProjectID, Path: countedPath, Role: DesignArchitectureFileRoleSource},
			{ProjectID: sliceDiffProjectID, Path: testPath, Role: DesignArchitectureFileRoleTest},
			{ProjectID: sliceDiffProjectID, Path: deletedPath, Role: DesignArchitectureFileRoleSource},
		},
		SliceFiles: []string{countedPath, testPath, deletedPath},
		Unchanged:  []string{"internal/store/workflow_architecture.go"},
		Limit:      designArchitectureMaxChangedLines,
	}
}

func measureSliceDiffVariant(t *testing.T, change func(in *sliceDiffInput)) (sliceDiffResult, error) {
	t.Helper()
	in := sliceDiffFixture()
	if change != nil {
		change(&in)
	}
	return measureSliceDiff(in)
}

// assertSliceDiffRefusal requires a typed refusal whose text names at least
// one of the given markers.
func assertSliceDiffRefusal(t *testing.T, err error, markers ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("measureSliceDiff accepted input it must refuse")
	}
	assertFailureKind(t, err, KindInvalidPayload)
	for _, marker := range markers {
		if strings.Contains(err.Error(), marker) {
			return
		}
	}
	t.Fatalf("refusal %v names none of the markers %q", err, markers)
}

func TestSliceDiff(t *testing.T) {
	t.Run("accepts the fixture and reports sorted measured paths", func(t *testing.T) {
		got, err := measureSliceDiffVariant(t, nil)
		if err != nil {
			t.Fatalf("valid slice diff refused: %v", err)
		}
		want := sliceDiffResult{
			Paths: []slicePathCounts{
				{Path: "internal/store/obsolete.go", Role: DesignArchitectureFileRoleSource, Additions: 0, Deletions: 30},
				{Path: "internal/store/worker_slice_diff.go", Role: DesignArchitectureFileRoleSource, Additions: 200, Deletions: 200},
				{Path: "internal/store/worker_slice_diff_test.go", Role: DesignArchitectureFileRoleTest, Additions: 120, Deletions: 0},
			},
			TotalLines: 430,
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("result = %+v, want %+v", got, want)
		}
		again, err := measureSliceDiffVariant(t, nil)
		if err != nil {
			t.Fatalf("second identical measurement refused: %v", err)
		}
		if !reflect.DeepEqual(got, again) {
			t.Fatalf("same input measured %+v then %+v; the checker is not deterministic", got, again)
		}
	})

	t.Run("enforces strict NUL framing on both streams", func(t *testing.T) {
		t.Run("raw stream without its final NUL refuses", func(t *testing.T) {
			_, err := measureSliceDiffVariant(t, func(in *sliceDiffInput) {
				in.RawDiff = []byte(sliceDiffRawRecord("100644", "100644", "M", "internal/store/worker_slice_diff.go"))
				in.RawDiff = in.RawDiff[:len(in.RawDiff)-1]
			})
			assertSliceDiffRefusal(t, err, "malformed")
		})

		t.Run("numstat stream without its final NUL refuses", func(t *testing.T) {
			_, err := measureSliceDiffVariant(t, func(in *sliceDiffInput) {
				in.Numstat = []byte(sliceDiffNumstatRecord("200", "200", "internal/store/worker_slice_diff.go"))
				in.Numstat = in.Numstat[:len(in.Numstat)-1]
			})
			assertSliceDiffRefusal(t, err, "malformed")
		})

		t.Run("empty record inside the raw stream refuses", func(t *testing.T) {
			_, err := measureSliceDiffVariant(t, func(in *sliceDiffInput) {
				in.RawDiff = []byte(strings.Join([]string{
					sliceDiffRawRecord("100644", "100644", "M", "internal/store/worker_slice_diff.go"),
					"\x00",
					sliceDiffRawRecord("100644", "000000", "D", "internal/store/obsolete.go"),
				}, ""))
			})
			assertSliceDiffRefusal(t, err, "malformed")
		})

		t.Run("header without a following path refuses", func(t *testing.T) {
			_, err := measureSliceDiffVariant(t, func(in *sliceDiffInput) {
				in.RawDiff = []byte(":100644 100644 " + sliceDiffOIDBase + " " + sliceDiffOIDEnd + " M\x00")
			})
			assertSliceDiffRefusal(t, err, "malformed")
		})

		t.Run("path record without a preceding header refuses", func(t *testing.T) {
			_, err := measureSliceDiffVariant(t, func(in *sliceDiffInput) {
				in.RawDiff = []byte(strings.Join([]string{
					sliceDiffRawRecord("100644", "100644", "M", "internal/store/worker_slice_diff.go"),
					"internal/store/orphan.go\x00",
				}, ""))
			})
			assertSliceDiffRefusal(t, err, "malformed")
		})

		t.Run("empty change set measures zero paths", func(t *testing.T) {
			got, err := measureSliceDiff(sliceDiffInput{ProjectID: sliceDiffProjectID, Limit: designArchitectureMaxChangedLines})
			if err != nil {
				t.Fatalf("empty change set refused: %v", err)
			}
			if len(got.Paths) != 0 || got.TotalLines != 0 {
				t.Fatalf("empty change set measured %+v, want zero paths and zero total", got)
			}
		})
	})

	t.Run("rejects malformed raw records", func(t *testing.T) {
		cases := []struct {
			name   string
			record string
			marker string
		}{
			{"header without the leading colon", "100644 100644 " + sliceDiffOIDBase + " " + sliceDiffOIDEnd + " M\x00internal/store/worker_slice_diff.go\x00", "malformed"},
			{"header with four fields", ":100644 100644 " + sliceDiffOIDBase + " " + sliceDiffOIDEnd + "\x00internal/store/worker_slice_diff.go\x00", "malformed"},
			{"header with six fields", ":100644 100644 " + sliceDiffOIDBase + " " + sliceDiffOIDEnd + " M extra\x00internal/store/worker_slice_diff.go\x00", "malformed"},
			{"repeated space between fields", ":100644  100644 " + sliceDiffOIDBase + " " + sliceDiffOIDEnd + " M\x00internal/store/worker_slice_diff.go\x00", "malformed"},
			{"tab between fields", ":100644\t100644 " + sliceDiffOIDBase + " " + sliceDiffOIDEnd + " M\x00internal/store/worker_slice_diff.go\x00", "malformed"},
			{"newline between fields", ":100644\n100644 " + sliceDiffOIDBase + " " + sliceDiffOIDEnd + " M\x00internal/store/worker_slice_diff.go\x00", "malformed"},
			{"trailing space after the status", ":100644 100644 " + sliceDiffOIDBase + " " + sliceDiffOIDEnd + " M \x00internal/store/worker_slice_diff.go\x00", "malformed"},
			{"header with an empty object field", ":100644 100644  " + sliceDiffOIDEnd + " M\x00internal/store/worker_slice_diff.go\x00", "malformed"},
			{"non-hex source object", ":100644 100644 " + strings.Repeat("z", 40) + " " + sliceDiffOIDEnd + " M\x00internal/store/worker_slice_diff.go\x00", "malformed"},
			{"non-hex destination object", ":100644 100644 " + sliceDiffOIDBase + " " + strings.Repeat("g", 40) + " M\x00internal/store/worker_slice_diff.go\x00", "malformed"},
			{"rename status with score", ":100644 100644 " + sliceDiffOIDBase + " " + sliceDiffOIDEnd + " R100\x00internal/store/worker_slice_diff.go\x00", "status"},
			{"copy status", ":100644 100644 " + sliceDiffOIDBase + " " + sliceDiffOIDEnd + " C\x00internal/store/worker_slice_diff.go\x00", "status"},
			{"typechange status", ":100644 100644 " + sliceDiffOIDBase + " " + sliceDiffOIDEnd + " T\x00internal/store/worker_slice_diff.go\x00", "status"},
			{"unknown status letter", ":100644 100644 " + sliceDiffOIDBase + " " + sliceDiffOIDEnd + " X\x00internal/store/worker_slice_diff.go\x00", "status"},
			{"symlink mode", ":000000 120000 " + sliceDiffOIDZero + " " + sliceDiffOIDEnd + " A\x00internal/store/link.go\x00", "mode"},
			{"gitlink mode", ":100644 160000 " + sliceDiffOIDBase + " " + sliceDiffOIDEnd + " M\x00internal/store/submodule\x00", "mode"},
			{"unsupported regular mode", ":100644 100664 " + sliceDiffOIDBase + " " + sliceDiffOIDEnd + " M\x00internal/store/worker_slice_diff.go\x00", "mode"},
			{"addition carrying a source mode", ":100644 100644 " + sliceDiffOIDBase + " " + sliceDiffOIDEnd + " A\x00internal/store/worker_slice_diff.go\x00", "mode"},
			{"deletion carrying a destination mode", ":100644 100644 " + sliceDiffOIDBase + " " + sliceDiffOIDEnd + " D\x00internal/store/obsolete.go\x00", "mode"},
			{"modification with a null source mode", ":000000 100644 " + sliceDiffOIDBase + " " + sliceDiffOIDEnd + " M\x00internal/store/worker_slice_diff.go\x00", "mode"},
			{"modification with a null destination mode", ":100644 000000 " + sliceDiffOIDBase + " " + sliceDiffOIDEnd + " M\x00internal/store/worker_slice_diff.go\x00", "mode"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				_, err := measureSliceDiffVariant(t, func(in *sliceDiffInput) {
					in.RawDiff = []byte(tc.record)
					in.Numstat = []byte{}
				})
				assertSliceDiffRefusal(t, err, tc.marker)
			})
		}
	})

	t.Run("enforces the path vocabulary exactly", func(t *testing.T) {
		t.Run("invalid UTF-8 path bytes refuse", func(t *testing.T) {
			_, err := measureSliceDiffVariant(t, func(in *sliceDiffInput) {
				in.RawDiff = []byte(sliceDiffRawRecord("100644", "100644", "M", "internal/store/bad\xff\xfename.go"))
				in.Numstat = []byte{}
			})
			assertSliceDiffRefusal(t, err, "UTF-8")
		})

		t.Run("tab in a path refuses with the bytes intact", func(t *testing.T) {
			tabPath := "dir\tfile.go"
			_, err := measureSliceDiffVariant(t, func(in *sliceDiffInput) {
				in.RawDiff = []byte(sliceDiffRawRecord("100644", "100644", "M", tabPath))
				in.Numstat = []byte(sliceDiffNumstatRecord("5", "5", tabPath))
			})
			assertSliceDiffRefusal(t, err, "control")
			// The refusal quotes the path with %q, so the escaped form proves
			// the complete path survived parsing without a split at the
			// control byte and without normalization into acceptance.
			if !strings.Contains(err.Error(), fmt.Sprintf("%q", tabPath)) {
				t.Fatalf("refusal %v does not carry the quoted full path %q; the parser split the path", err, fmt.Sprintf("%q", tabPath))
			}
		})

		t.Run("newline in a path refuses with the bytes intact", func(t *testing.T) {
			newlinePath := "dir\nfile.go"
			_, err := measureSliceDiffVariant(t, func(in *sliceDiffInput) {
				in.RawDiff = []byte(sliceDiffRawRecord("100644", "100644", "M", newlinePath))
				in.Numstat = []byte(sliceDiffNumstatRecord("5", "5", newlinePath))
			})
			assertSliceDiffRefusal(t, err, "control")
			// NUL framing alone delimits records: the newline must arrive
			// inside the path token and refuse as a control byte, never act
			// as a record or field separator.
			if !strings.Contains(err.Error(), fmt.Sprintf("%q", newlinePath)) {
				t.Fatalf("refusal %v does not carry the quoted full path %q; the parser split the path", err, fmt.Sprintf("%q", newlinePath))
			}
		})

		t.Run("delete byte in a path refuses", func(t *testing.T) {
			_, err := measureSliceDiffVariant(t, func(in *sliceDiffInput) {
				in.RawDiff = []byte(sliceDiffRawRecord("100644", "100644", "M", "internal/store/x\x7fy.go"))
				in.Numstat = []byte{}
			})
			assertSliceDiffRefusal(t, err, "control")
		})

		t.Run("unsafe path shapes refuse", func(t *testing.T) {
			cases := []struct {
				name   string
				path   string
				marker string
			}{
				{"parent traversal", "../escape.go", "normalized"},
				{"absolute path", "/etc/passwd", "normalized"},
				{"trailing separator", "internal/store/", "normalized"},
				{"empty segment", "internal//store.go", "normalized"},
				{"self segment", "internal/./store.go", "normalized"},
				{"glob spelling", "internal/*.go", "glob"},
				{"bracket glob", "internal/store/[ab].go", "glob"},
				{"tmp first segment", "tmp/scratch.go", "tmp"},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					_, err := measureSliceDiffVariant(t, func(in *sliceDiffInput) {
						in.RawDiff = []byte(sliceDiffRawRecord("100644", "100644", "M", tc.path))
						in.Numstat = []byte{}
					})
					assertSliceDiffRefusal(t, err, tc.marker)
				})
			}
		})

		t.Run("valid multibyte UTF-8 path bytes survive exact", func(t *testing.T) {
			multibytePath := "internal/store/国際化.go"
			got, err := measureSliceDiffVariant(t, func(in *sliceDiffInput) {
				in.RawDiff = []byte(sliceDiffRawRecord("000000", "100644", "A", multibytePath))
				in.Numstat = []byte(sliceDiffNumstatRecord("9", "0", multibytePath))
				in.NewBlobs[multibytePath] = sliceDiffTextBlob(1)
				in.Files = append(in.Files, WorkflowDesignArchitectureFile{ProjectID: sliceDiffProjectID, Path: multibytePath, Role: DesignArchitectureFileRoleSource})
				in.SliceFiles = append(in.SliceFiles, multibytePath)
			})
			if err != nil {
				t.Fatalf("multibyte path refused: %v", err)
			}
			if len(got.Paths) != 1 || got.Paths[0].Path != multibytePath {
				t.Fatalf("result paths = %+v, want the exact multibyte path %q", got.Paths, multibytePath)
			}
		})

		t.Run("duplicate raw path refuses", func(t *testing.T) {
			_, err := measureSliceDiffVariant(t, func(in *sliceDiffInput) {
				in.RawDiff = []byte(strings.Join([]string{
					sliceDiffRawRecord("100644", "100644", "M", "internal/store/worker_slice_diff.go"),
					sliceDiffRawRecord("100644", "100644", "M", "internal/store/worker_slice_diff.go"),
				}, ""))
				in.Numstat = []byte(strings.Join([]string{
					sliceDiffNumstatRecord("200", "200", "internal/store/worker_slice_diff.go"),
					sliceDiffNumstatRecord("200", "200", "internal/store/worker_slice_diff.go"),
				}, ""))
			})
			assertSliceDiffRefusal(t, err, "duplicate")
		})
	})

	t.Run("charges a rename as deletion plus addition at both endpoints", func(t *testing.T) {
		const (
			oldName = "internal/store/renamed_old.go"
			newName = "internal/store/renamed_new.go"
		)
		renameInput := func() sliceDiffInput {
			return sliceDiffInput{
				ProjectID: sliceDiffProjectID,
				RawDiff: []byte(strings.Join([]string{
					sliceDiffRawRecord("100644", "000000", "D", oldName),
					sliceDiffRawRecord("000000", "100644", "A", newName),
				}, "")),
				Numstat: []byte(strings.Join([]string{
					sliceDiffNumstatRecord("0", "12", oldName),
					sliceDiffNumstatRecord("34", "0", newName),
				}, "")),
				OldBlobs: map[string][]byte{oldName: sliceDiffTextBlob(2)},
				NewBlobs: map[string][]byte{newName: sliceDiffTextBlob(3)},
				Files: []WorkflowDesignArchitectureFile{
					{ProjectID: sliceDiffProjectID, Path: oldName, Role: DesignArchitectureFileRoleSource},
					{ProjectID: sliceDiffProjectID, Path: newName, Role: DesignArchitectureFileRoleSource},
				},
				SliceFiles: []string{oldName, newName},
				Limit:      designArchitectureMaxChangedLines,
			}
		}

		t.Run("both endpoints count when both are approved", func(t *testing.T) {
			got, err := measureSliceDiff(renameInput())
			if err != nil {
				t.Fatalf("approved rename refused: %v", err)
			}
			want := sliceDiffResult{
				Paths: []slicePathCounts{
					{Path: newName, Role: DesignArchitectureFileRoleSource, Additions: 34, Deletions: 0},
					{Path: oldName, Role: DesignArchitectureFileRoleSource, Additions: 0, Deletions: 12},
				},
				TotalLines: 46,
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("rename result = %+v, want %+v", got, want)
			}
		})

		t.Run("new endpoint without an approved role refuses", func(t *testing.T) {
			in := renameInput()
			in.Files = in.Files[:1]
			_, err := measureSliceDiff(in)
			assertSliceDiffRefusal(t, err, "role")
		})

		t.Run("old endpoint outside the slice set refuses", func(t *testing.T) {
			in := renameInput()
			in.SliceFiles = []string{newName}
			_, err := measureSliceDiff(in)
			assertSliceDiffRefusal(t, err, "slice")
		})
	})

	t.Run("scope checks cover test and generated paths", func(t *testing.T) {
		t.Run("test path outside the slice set refuses", func(t *testing.T) {
			_, err := measureSliceDiffVariant(t, func(in *sliceDiffInput) {
				in.SliceFiles = []string{"internal/store/worker_slice_diff.go", "internal/store/obsolete.go"}
			})
			assertSliceDiffRefusal(t, err, "slice")
		})

		t.Run("generated path outside the slice set refuses", func(t *testing.T) {
			const generatedPath = "assets/bundle.min.js"
			_, err := measureSliceDiffVariant(t, func(in *sliceDiffInput) {
				in.RawDiff = []byte(sliceDiffRawRecord("100644", "100644", "M", generatedPath))
				in.Numstat = []byte(sliceDiffNumstatRecord("-", "-", generatedPath))
				in.Files = []WorkflowDesignArchitectureFile{{ProjectID: sliceDiffProjectID, Path: generatedPath, Role: DesignArchitectureFileRoleGenerated}}
			})
			assertSliceDiffRefusal(t, err, "slice")
		})

		t.Run("changed path declared unchanged refuses", func(t *testing.T) {
			_, err := measureSliceDiffVariant(t, func(in *sliceDiffInput) {
				in.Unchanged = []string{"internal/store/worker_slice_diff.go"}
			})
			assertSliceDiffRefusal(t, err, "unchanged")
		})

		t.Run("path absent from the approved file map refuses", func(t *testing.T) {
			_, err := measureSliceDiffVariant(t, func(in *sliceDiffInput) {
				in.Files = []WorkflowDesignArchitectureFile{
					{ProjectID: sliceDiffProjectID, Path: "internal/store/worker_slice_diff.go", Role: DesignArchitectureFileRoleSource},
					{ProjectID: sliceDiffProjectID, Path: "internal/store/obsolete.go", Role: DesignArchitectureFileRoleSource},
				}
			})
			assertSliceDiffRefusal(t, err, "role")
		})

		t.Run("generated-sounding name without a map entry refuses", func(t *testing.T) {
			const nameOnlyPath = "internal/store/generated_helpers.go"
			_, err := measureSliceDiffVariant(t, func(in *sliceDiffInput) {
				in.RawDiff = []byte(sliceDiffRawRecord("000000", "100644", "A", nameOnlyPath))
				in.Numstat = []byte(sliceDiffNumstatRecord("5", "0", nameOnlyPath))
				in.SliceFiles = append(in.SliceFiles, nameOnlyPath)
				in.NewBlobs[nameOnlyPath] = sliceDiffTextBlob(1)
			})
			assertSliceDiffRefusal(t, err, "role")
		})

		t.Run("another Project's map entry does not approve the path", func(t *testing.T) {
			_, err := measureSliceDiffVariant(t, func(in *sliceDiffInput) {
				in.Files = []WorkflowDesignArchitectureFile{
					{ProjectID: "other-project", Path: "internal/store/worker_slice_diff.go", Role: DesignArchitectureFileRoleSource},
					{ProjectID: sliceDiffProjectID, Path: "internal/store/worker_slice_diff_test.go", Role: DesignArchitectureFileRoleTest},
					{ProjectID: sliceDiffProjectID, Path: "internal/store/obsolete.go", Role: DesignArchitectureFileRoleSource},
				}
			})
			assertSliceDiffRefusal(t, err, "role")
		})

		t.Run("unknown role text refuses", func(t *testing.T) {
			_, err := measureSliceDiffVariant(t, func(in *sliceDiffInput) {
				in.Files = []WorkflowDesignArchitectureFile{
					{ProjectID: sliceDiffProjectID, Path: "internal/store/worker_slice_diff.go", Role: "spec"},
					{ProjectID: sliceDiffProjectID, Path: "internal/store/worker_slice_diff_test.go", Role: DesignArchitectureFileRoleTest},
					{ProjectID: sliceDiffProjectID, Path: "internal/store/obsolete.go", Role: DesignArchitectureFileRoleSource},
				}
			})
			assertSliceDiffRefusal(t, err, "role")
		})

		t.Run("duplicate matching file-map entry refuses instead of reclassifying", func(t *testing.T) {
			_, err := measureSliceDiffVariant(t, func(in *sliceDiffInput) {
				in.Files = append(in.Files, WorkflowDesignArchitectureFile{ProjectID: sliceDiffProjectID, Path: "internal/store/worker_slice_diff.go", Role: DesignArchitectureFileRoleTest})
			})
			assertSliceDiffRefusal(t, err, "duplicate")
		})
	})

	t.Run("validates source blobs without restricting generated or test blobs", func(t *testing.T) {
		t.Run("added source blob containing NUL refuses", func(t *testing.T) {
			_, err := measureSliceDiffVariant(t, func(in *sliceDiffInput) {
				for i := range in.Files {
					if in.Files[i].Path == "internal/store/worker_slice_diff_test.go" {
						in.Files[i].Role = DesignArchitectureFileRoleSource
					}
				}
				in.NewBlobs["internal/store/worker_slice_diff_test.go"] = []byte("text\x00more")
			})
			assertSliceDiffRefusal(t, err, "UTF-8", "NUL")
		})

		t.Run("modified source old blob with invalid UTF-8 refuses", func(t *testing.T) {
			_, err := measureSliceDiffVariant(t, func(in *sliceDiffInput) {
				in.OldBlobs["internal/store/worker_slice_diff.go"] = []byte{'f', 0xff, 'e', '\n'}
			})
			assertSliceDiffRefusal(t, err, "UTF-8", "NUL")
		})

		t.Run("deleted source old blob containing NUL refuses", func(t *testing.T) {
			_, err := measureSliceDiffVariant(t, func(in *sliceDiffInput) {
				in.OldBlobs["internal/store/obsolete.go"] = []byte("legacy\x00text")
			})
			assertSliceDiffRefusal(t, err, "UTF-8", "NUL")
		})

		t.Run("added source path without a new blob refuses", func(t *testing.T) {
			_, err := measureSliceDiffVariant(t, func(in *sliceDiffInput) {
				for i := range in.Files {
					if in.Files[i].Path == "internal/store/worker_slice_diff_test.go" {
						in.Files[i].Role = DesignArchitectureFileRoleSource
					}
				}
			})
			assertSliceDiffRefusal(t, err, "blob")
		})

		t.Run("deleted source path without an old blob refuses", func(t *testing.T) {
			_, err := measureSliceDiffVariant(t, func(in *sliceDiffInput) {
				delete(in.OldBlobs, "internal/store/obsolete.go")
			})
			assertSliceDiffRefusal(t, err, "blob")
		})

		t.Run("modified source path missing one of its two blobs refuses", func(t *testing.T) {
			_, err := measureSliceDiffVariant(t, func(in *sliceDiffInput) {
				delete(in.NewBlobs, "internal/store/worker_slice_diff.go")
			})
			assertSliceDiffRefusal(t, err, "blob")
		})

		t.Run("binary generated and test content measure as excluded zero", func(t *testing.T) {
			const generatedPath = "assets/bundle.min.js"
			const testPath = "internal/store/worker_slice_diff_test.go"
			in := sliceDiffInput{
				ProjectID: sliceDiffProjectID,
				RawDiff: []byte(strings.Join([]string{
					sliceDiffRawRecord("100644", "100644", "M", generatedPath),
					sliceDiffRawRecord("100644", "100644", "M", testPath),
				}, "")),
				Numstat: []byte(strings.Join([]string{
					sliceDiffNumstatRecord("-", "-", generatedPath),
					sliceDiffNumstatRecord("-", "-", testPath),
				}, "")),
				OldBlobs: map[string][]byte{
					generatedPath: []byte{0x00, 0x01, 0x02},
					testPath:      []byte{0xff, 0xfe, 0x00},
				},
				NewBlobs: map[string][]byte{
					generatedPath: []byte{0x03, 0x00},
					testPath:      []byte{0x80, 0x81},
				},
				Files: []WorkflowDesignArchitectureFile{
					{ProjectID: sliceDiffProjectID, Path: generatedPath, Role: DesignArchitectureFileRoleGenerated},
					{ProjectID: sliceDiffProjectID, Path: testPath, Role: DesignArchitectureFileRoleTest},
				},
				SliceFiles: []string{generatedPath, testPath},
				Limit:      designArchitectureMaxChangedLines,
			}
			got, err := measureSliceDiff(in)
			if err != nil {
				t.Fatalf("binary generated and test content refused: %v", err)
			}
			want := sliceDiffResult{
				Paths: []slicePathCounts{
					{Path: generatedPath, Role: DesignArchitectureFileRoleGenerated, Additions: 0, Deletions: 0},
					{Path: testPath, Role: DesignArchitectureFileRoleTest, Additions: 0, Deletions: 0},
				},
				TotalLines: 0,
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("binary result = %+v, want %+v", got, want)
			}
		})

		t.Run("added source path with an empty new blob counts zero", func(t *testing.T) {
			got, err := measureSliceDiffVariant(t, func(in *sliceDiffInput) {
				for i := range in.Files {
					if in.Files[i].Path == "internal/store/worker_slice_diff_test.go" {
						in.Files[i].Role = DesignArchitectureFileRoleSource
					}
				}
				in.Numstat = []byte(sliceDiffNumstatRecord("0", "0", "internal/store/worker_slice_diff_test.go"))
				in.NewBlobs["internal/store/worker_slice_diff_test.go"] = []byte{}
				in.RawDiff = []byte(sliceDiffRawRecord("000000", "100644", "A", "internal/store/worker_slice_diff_test.go"))
			})
			if err != nil {
				t.Fatalf("empty new source blob refused: %v", err)
			}
			if got.TotalLines != 0 || len(got.Paths) != 1 || got.Paths[0].Additions != 0 || got.Paths[0].Deletions != 0 {
				t.Fatalf("empty blob result = %+v, want one zero-count source path", got)
			}
		})
	})

	t.Run("demands one exact numstat record per changed path", func(t *testing.T) {
		t.Run("record for a path outside the raw set refuses", func(t *testing.T) {
			_, err := measureSliceDiffVariant(t, func(in *sliceDiffInput) {
				in.Numstat = append(in.Numstat, []byte(sliceDiffNumstatRecord("1", "1", "internal/store/ghost.go"))...)
			})
			assertSliceDiffRefusal(t, err, "numstat")
		})

		t.Run("missing record for a changed path refuses", func(t *testing.T) {
			_, err := measureSliceDiffVariant(t, func(in *sliceDiffInput) {
				in.Numstat = []byte(strings.Join([]string{
					sliceDiffNumstatRecord("200", "200", "internal/store/worker_slice_diff.go"),
					sliceDiffNumstatRecord("120", "0", "internal/store/worker_slice_diff_test.go"),
				}, ""))
			})
			assertSliceDiffRefusal(t, err, "numstat")
		})

		t.Run("duplicate numstat record refuses", func(t *testing.T) {
			_, err := measureSliceDiffVariant(t, func(in *sliceDiffInput) {
				in.Numstat = []byte(strings.Join([]string{
					sliceDiffNumstatRecord("200", "200", "internal/store/worker_slice_diff.go"),
					sliceDiffNumstatRecord("200", "200", "internal/store/worker_slice_diff.go"),
					sliceDiffNumstatRecord("120", "0", "internal/store/worker_slice_diff_test.go"),
					sliceDiffNumstatRecord("0", "30", "internal/store/obsolete.go"),
				}, ""))
			})
			assertSliceDiffRefusal(t, err, "duplicate")
		})

		t.Run("binary marker on a counted source path refuses", func(t *testing.T) {
			_, err := measureSliceDiffVariant(t, func(in *sliceDiffInput) {
				in.Numstat = []byte(strings.Join([]string{
					sliceDiffNumstatRecord("-", "-", "internal/store/worker_slice_diff.go"),
					sliceDiffNumstatRecord("120", "0", "internal/store/worker_slice_diff_test.go"),
					sliceDiffNumstatRecord("0", "30", "internal/store/obsolete.go"),
				}, ""))
			})
			assertSliceDiffRefusal(t, err, "binary")
		})

		t.Run("malformed numstat values refuse", func(t *testing.T) {
			cases := []struct {
				name      string
				additions string
				deletions string
				path      string
				marker    string
			}{
				{"mixed marker and number", "120", "-", "internal/store/worker_slice_diff_test.go", "numstat"},
				{"non-numeric digits", "12x", "200", "internal/store/worker_slice_diff.go", "numstat"},
				{"decimal value", "1.5", "200", "internal/store/worker_slice_diff.go", "numstat"},
				{"negative value", "0", "-1", "internal/store/obsolete.go", "numstat"},
				{"empty field", "", "200", "internal/store/worker_slice_diff.go", "numstat"},
				{"plus-signed value", "+3", "0", "internal/store/worker_slice_diff_test.go", "numstat"},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					_, err := measureSliceDiffVariant(t, func(in *sliceDiffInput) {
						records := []string{}
						for _, record := range strings.Split(strings.TrimSuffix(string(in.Numstat), "\x00"), "\x00") {
							parts := strings.SplitN(record, "\t", 3)
							if len(parts) == 3 && parts[2] == tc.path {
								records = append(records, sliceDiffNumstatRecord(tc.additions, tc.deletions, tc.path))
								continue
							}
							records = append(records, record+"\x00")
						}
						in.Numstat = []byte(strings.Join(records, ""))
					})
					assertSliceDiffRefusal(t, err, tc.marker)
				})
			}
		})
	})

	t.Run("sums additions plus deletions against the declared limit", func(t *testing.T) {
		singleReplacement := func(additions, deletions string) sliceDiffInput {
			const countedPath = "internal/store/worker_slice_diff.go"
			return sliceDiffInput{
				ProjectID: sliceDiffProjectID,
				RawDiff:   []byte(sliceDiffRawRecord("100644", "100644", "M", countedPath)),
				Numstat:   []byte(sliceDiffNumstatRecord(additions, deletions, countedPath)),
				OldBlobs:  map[string][]byte{countedPath: sliceDiffTextBlob(1)},
				NewBlobs:  map[string][]byte{countedPath: sliceDiffTextBlob(1)},
				Files: []WorkflowDesignArchitectureFile{
					{ProjectID: sliceDiffProjectID, Path: countedPath, Role: DesignArchitectureFileRoleSource},
				},
				SliceFiles: []string{countedPath},
				Limit:      designArchitectureMaxChangedLines,
			}
		}

		t.Run("exactly the declared limit passes", func(t *testing.T) {
			got, err := measureSliceDiff(singleReplacement("250", "250"))
			if err != nil {
				t.Fatalf("limit-boundary replacement refused: %v", err)
			}
			if got.TotalLines != 500 {
				t.Fatalf("250 added plus 250 deleted counted %d lines, want 500 and not the net 0", got.TotalLines)
			}
		})

		t.Run("limit plus one refuses", func(t *testing.T) {
			_, err := measureSliceDiff(singleReplacement("251", "250"))
			assertSliceDiffRefusal(t, err, "limit")
		})

		t.Run("declared limit smaller than the measurement refuses", func(t *testing.T) {
			in := singleReplacement("250", "250")
			in.Limit = 499
			_, err := measureSliceDiff(in)
			assertSliceDiffRefusal(t, err, "limit")
		})

		t.Run("declared limit outside its closed bounds refuses", func(t *testing.T) {
			for _, limit := range []int{designArchitectureMinChangedLines - 1, designArchitectureMaxChangedLines + 1} {
				in := sliceDiffFixture()
				in.Limit = limit
				_, err := measureSliceDiff(in)
				assertSliceDiffRefusal(t, err, "limit")
			}
		})

		t.Run("integer overflow refuses", func(t *testing.T) {
			t.Run("field beyond the integer range", func(t *testing.T) {
				_, err := measureSliceDiff(singleReplacement("99999999999999999999", "0"))
				assertSliceDiffRefusal(t, err, "overflow")
			})

			t.Run("pair sum overflow", func(t *testing.T) {
				_, err := measureSliceDiff(singleReplacement("9223372036854775807", "1"))
				assertSliceDiffRefusal(t, err, "overflow")
			})

			t.Run("total sum overflow", func(t *testing.T) {
				const (
					firstPath  = "internal/store/first.go"
					secondPath = "internal/store/second.go"
				)
				in := sliceDiffInput{
					ProjectID: sliceDiffProjectID,
					RawDiff: []byte(strings.Join([]string{
						sliceDiffRawRecord("100644", "100644", "M", firstPath),
						sliceDiffRawRecord("100644", "100644", "M", secondPath),
					}, "")),
					Numstat: []byte(strings.Join([]string{
						sliceDiffNumstatRecord("4611686018427387904", "0", firstPath),
						sliceDiffNumstatRecord("4611686018427387904", "0", secondPath),
					}, "")),
					OldBlobs: map[string][]byte{
						firstPath:  sliceDiffTextBlob(1),
						secondPath: sliceDiffTextBlob(1),
					},
					NewBlobs: map[string][]byte{
						firstPath:  sliceDiffTextBlob(1),
						secondPath: sliceDiffTextBlob(1),
					},
					Files: []WorkflowDesignArchitectureFile{
						{ProjectID: sliceDiffProjectID, Path: firstPath, Role: DesignArchitectureFileRoleSource},
						{ProjectID: sliceDiffProjectID, Path: secondPath, Role: DesignArchitectureFileRoleSource},
					},
					SliceFiles: []string{firstPath, secondPath},
					Limit:      designArchitectureMaxChangedLines,
				}
				_, err := measureSliceDiff(in)
				assertSliceDiffRefusal(t, err, "overflow")
			})
		})
	})

	t.Run("accepts regular executable files only", func(t *testing.T) {
		t.Run("added executable file counts", func(t *testing.T) {
			const execPath = "internal/store/run-helper.sh"
			got, err := measureSliceDiffVariant(t, func(in *sliceDiffInput) {
				in.RawDiff = []byte(sliceDiffRawRecord("000000", "100755", "A", execPath))
				in.Numstat = []byte(sliceDiffNumstatRecord("9", "0", execPath))
				in.NewBlobs[execPath] = sliceDiffTextBlob(1)
				in.Files = append(in.Files, WorkflowDesignArchitectureFile{ProjectID: sliceDiffProjectID, Path: execPath, Role: DesignArchitectureFileRoleSource})
				in.SliceFiles = append(in.SliceFiles, execPath)
			})
			if err != nil {
				t.Fatalf("added executable file refused: %v", err)
			}
			if got.TotalLines != 9 {
				t.Fatalf("executable addition counted %d lines, want 9", got.TotalLines)
			}
		})

		t.Run("mode flip between regular and executable counts as modification", func(t *testing.T) {
			const flipPath = "internal/store/worker_slice_diff.go"
			got, err := measureSliceDiffVariant(t, func(in *sliceDiffInput) {
				in.RawDiff = []byte(sliceDiffRawRecord("100644", "100755", "M", flipPath))
				in.Numstat = []byte(sliceDiffNumstatRecord("5", "5", flipPath))
			})
			if err != nil {
				t.Fatalf("regular-to-executable mode flip refused: %v", err)
			}
			if got.TotalLines != 10 {
				t.Fatalf("mode flip counted %d lines, want 10", got.TotalLines)
			}
		})
	})
}

// TestSliceDiffOrderIndependence proves the bounded deterministic properties:
// any ordering of the raw and numstat records measures the same sorted
// result, and the counted total always equals the summed addition and
// deletion cost of the counted source paths.
func TestSliceDiffOrderIndependence(t *testing.T) {
	const (
		sourceAlpha = "internal/store/alpha.go"
		testBeta    = "internal/store/beta_test.go"
		sourceGamma = "internal/store/gamma.go"
		sourceDelta = "internal/store/delta.go"
		generatedJS = "assets/bundle.js"
		sourceEps   = "internal/store/epsilon.go"
	)

	scenario := func() sliceDiffInput {
		return sliceDiffInput{
			ProjectID: sliceDiffProjectID,
			RawDiff: []byte(strings.Join([]string{
				sliceDiffRawRecord("100644", "100644", "M", sourceAlpha),
				sliceDiffRawRecord("100644", "100644", "M", testBeta),
				sliceDiffRawRecord("000000", "100644", "A", sourceGamma),
				sliceDiffRawRecord("100644", "000000", "D", sourceDelta),
				sliceDiffRawRecord("100644", "100644", "M", generatedJS),
				sliceDiffRawRecord("100644", "100644", "M", sourceEps),
			}, "")),
			Numstat: []byte(strings.Join([]string{
				sliceDiffNumstatRecord("10", "5", sourceAlpha),
				sliceDiffNumstatRecord("-", "-", testBeta),
				sliceDiffNumstatRecord("40", "0", sourceGamma),
				sliceDiffNumstatRecord("0", "7", sourceDelta),
				sliceDiffNumstatRecord("-", "-", generatedJS),
				sliceDiffNumstatRecord("1", "1", sourceEps),
			}, "")),
			OldBlobs: map[string][]byte{
				sourceAlpha: sliceDiffTextBlob(2),
				testBeta:    []byte{0x00, 0x01},
				sourceDelta: sliceDiffTextBlob(2),
				generatedJS: []byte{0x00, 0x02},
				sourceEps:   sliceDiffTextBlob(2),
			},
			NewBlobs: map[string][]byte{
				sourceAlpha: sliceDiffTextBlob(3),
				testBeta:    []byte{0xff},
				sourceGamma: sliceDiffTextBlob(2),
				generatedJS: []byte{0x00, 0x03},
				sourceEps:   sliceDiffTextBlob(3),
			},
			Files: []WorkflowDesignArchitectureFile{
				{ProjectID: sliceDiffProjectID, Path: sourceAlpha, Role: DesignArchitectureFileRoleSource},
				{ProjectID: sliceDiffProjectID, Path: testBeta, Role: DesignArchitectureFileRoleTest},
				{ProjectID: sliceDiffProjectID, Path: sourceGamma, Role: DesignArchitectureFileRoleSource},
				{ProjectID: sliceDiffProjectID, Path: sourceDelta, Role: DesignArchitectureFileRoleSource},
				{ProjectID: sliceDiffProjectID, Path: generatedJS, Role: DesignArchitectureFileRoleGenerated},
				{ProjectID: sliceDiffProjectID, Path: sourceEps, Role: DesignArchitectureFileRoleSource},
			},
			SliceFiles: []string{sourceAlpha, testBeta, sourceGamma, sourceDelta, generatedJS, sourceEps},
			Limit:      designArchitectureMaxChangedLines,
		}
	}

	want := sliceDiffResult{
		Paths: []slicePathCounts{
			{Path: generatedJS, Role: DesignArchitectureFileRoleGenerated, Additions: 0, Deletions: 0},
			{Path: sourceAlpha, Role: DesignArchitectureFileRoleSource, Additions: 10, Deletions: 5},
			{Path: testBeta, Role: DesignArchitectureFileRoleTest, Additions: 0, Deletions: 0},
			{Path: sourceDelta, Role: DesignArchitectureFileRoleSource, Additions: 0, Deletions: 7},
			{Path: sourceEps, Role: DesignArchitectureFileRoleSource, Additions: 1, Deletions: 1},
			{Path: sourceGamma, Role: DesignArchitectureFileRoleSource, Additions: 40, Deletions: 0},
		},
		TotalLines: 64,
	}

	// A raw change is an atomic two-token record (header then path), so the
	// raw permutation moves whole records; numstat records are single tokens.
	sliceDiffReordered := func(stream string, order []int, paired bool) string {
		trimmed := strings.TrimSuffix(stream, "\x00")
		if trimmed == "" {
			return stream
		}
		tokens := strings.Split(trimmed, "\x00")
		step := 1
		if paired {
			step = 2
		}
		records := make([]string, 0, len(tokens))
		for i := 0; i < len(tokens); i += step {
			record := tokens[i]
			if paired {
				record = tokens[i] + "\x00" + tokens[i+1]
			}
			records = append(records, record)
		}
		var b strings.Builder
		for _, i := range order {
			b.WriteString(records[i])
			b.WriteString("\x00")
		}
		return b.String()
	}

	in := scenario()
	rawLen := len(strings.Split(strings.TrimSuffix(string(in.RawDiff), "\x00"), "\x00")) / 2
	statLen := len(strings.Split(strings.TrimSuffix(string(in.Numstat), "\x00"), "\x00"))
	reverse := func(n int) []int {
		order := make([]int, n)
		for i := range order {
			order[i] = n - 1 - i
		}
		return order
	}
	rotate := func(n, k int) []int {
		order := make([]int, n)
		for i := range order {
			order[i] = (i + k) % n
		}
		return order
	}

	permutations := []struct {
		name string
		raw  []int
		stat []int
	}{
		{"identity", rotate(rawLen, 0), rotate(statLen, 0)},
		{"both reversed", reverse(rawLen), reverse(statLen)},
		{"raw reversed only", reverse(rawLen), rotate(statLen, 0)},
		{"numstat reversed only", rotate(rawLen, 0), reverse(statLen)},
		{"both rotated by one", rotate(rawLen, 1), rotate(statLen, 1)},
		{"raw rotated two and numstat three", rotate(rawLen, 2), rotate(statLen, 3)},
	}
	shuffleSource := rand.New(rand.NewSource(9))
	for i := 0; i < 5; i++ {
		permutations = append(permutations, struct {
			name string
			raw  []int
			stat []int
		}{fmt.Sprintf("seeded shuffle %d", i+1), shuffleSource.Perm(rawLen), shuffleSource.Perm(statLen)})
	}

	for _, perm := range permutations {
		t.Run(perm.name, func(t *testing.T) {
			in := scenario()
			in.RawDiff = []byte(sliceDiffReordered(string(in.RawDiff), perm.raw, true))
			in.Numstat = []byte(sliceDiffReordered(string(in.Numstat), perm.stat, false))

			got, err := measureSliceDiff(in)
			if err != nil {
				t.Fatalf("reordered measurement refused: %v", err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("reordered result = %+v, want the order-independent %+v", got, want)
			}
			if !sort.SliceIsSorted(got.Paths, func(i, j int) bool { return got.Paths[i].Path < got.Paths[j].Path }) {
				t.Fatalf("result paths are not sorted by path: %+v", got.Paths)
			}
			summed := 0
			for _, path := range got.Paths {
				if path.Role != DesignArchitectureFileRoleSource {
					continue
				}
				summed += path.Additions + path.Deletions
			}
			if summed != got.TotalLines {
				t.Fatalf("counted source paths sum to %d but the total reports %d", summed, got.TotalLines)
			}
		})
	}

	t.Run("addition plus deletion cost identity", func(t *testing.T) {
		cases := []struct {
			name      string
			status    string
			srcMode   string
			dstMode   string
			additions string
			deletions string
			wantTotal int
		}{
			{"pure addition", "A", "000000", "100644", "5", "0", 5},
			{"pure deletion", "D", "100644", "000000", "0", "5", 5},
			{"equal replacement", "M", "100644", "100644", "250", "250", 500},
			{"mixed replacement", "M", "100644", "100644", "3", "7", 10},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				const countedPath = "internal/store/cost.go"
				in := sliceDiffInput{
					ProjectID: sliceDiffProjectID,
					RawDiff:   []byte(sliceDiffRawRecord(tc.srcMode, tc.dstMode, tc.status, countedPath)),
					Numstat:   []byte(sliceDiffNumstatRecord(tc.additions, tc.deletions, countedPath)),
					OldBlobs:  map[string][]byte{},
					NewBlobs:  map[string][]byte{},
					Files: []WorkflowDesignArchitectureFile{
						{ProjectID: sliceDiffProjectID, Path: countedPath, Role: DesignArchitectureFileRoleSource},
					},
					SliceFiles: []string{countedPath},
					Limit:      tc.wantTotal,
				}
				if tc.status != "A" {
					in.OldBlobs[countedPath] = sliceDiffTextBlob(1)
				}
				if tc.status != "D" {
					in.NewBlobs[countedPath] = sliceDiffTextBlob(1)
				}
				got, err := measureSliceDiff(in)
				if err != nil {
					t.Fatalf("cost identity case refused: %v", err)
				}
				if got.TotalLines != tc.wantTotal {
					t.Fatalf("total = %d, want the addition plus deletion cost %d and never a net count", got.TotalLines, tc.wantTotal)
				}
				if len(got.Paths) != 1 {
					t.Fatalf("paths = %+v, want exactly one measured path", got.Paths)
				}
			})
		}
	})
}
