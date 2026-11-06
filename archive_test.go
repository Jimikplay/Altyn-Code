package audit

import (
	"archive/zip"
	"bytes"
	"os"
	"testing"
)

func zipFixture(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	w := zip.NewWriter(&buffer)
	for name, contents := range entries {
		file, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte(contents)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestLoadZIPStripsSingleRepositoryRoot(t *testing.T) {
	data := zipFixture(t, map[string]string{
		"project-123/go.mod":         "module example.com/sample\n",
		"project-123/sample.go":      sample,
		"project-123/sample_test.go": "package sample\n",
	})
	files, err := LoadZIP(data)
	if err != nil {
		t.Fatal(err)
	}
	if string(files["sample.go"]) != sample || len(files) != 3 {
		t.Fatalf("unexpected files: %v", files)
	}
}

func TestLoadZIPRejectsUnsafeNames(t *testing.T) {
	for _, name := range []string{"../secrets.go", "/absolute.go", "project/../../escape.go", "C:\\evil.go", "C:/escape.go", "CON.go", ".git/hooks/post-checkout"} {
		t.Run(name, func(t *testing.T) {
			data := zipFixture(t, map[string]string{"go.mod": "module sample\n", name: "malicious"})
			if _, err := LoadZIP(data); err == nil {
				t.Fatal("unsafe archive entry was accepted")
			}
		})
	}
}

func TestLoadZIPRejectsSymlinks(t *testing.T) {
	var buffer bytes.Buffer
	w := zip.NewWriter(&buffer)
	module, err := w.Create("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = module.Write([]byte("module example.com/sample\n"))
	header := &zip.FileHeader{Name: "sample.go"}
	header.SetMode(os.ModeSymlink | 0777)
	link, err := w.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = link.Write([]byte("../../secret"))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadZIP(buffer.Bytes()); err == nil {
		t.Fatal("symlink was accepted")
	}
}

func TestLoadZIPRejectsTooMuchUncompressedData(t *testing.T) {
	data := zipFixture(t, map[string]string{"go.mod": "module sample\n", "huge.go": string(bytes.Repeat([]byte{'x'}, 512*1024))})
	if _, err := LoadZIP(data); err == nil {
		t.Fatal("oversized source was accepted")
	}
}
