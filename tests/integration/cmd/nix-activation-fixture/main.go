// nix-activation-fixture renders a real guest capture with the production adapter.
// It is copied into the disposable Incus guest; no project Nix code runs on the VM host.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lgvo/p.ai/internal/nixenv"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "nix-activation-fixture:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 3 {
		return fmt.Errorf("usage: nix-activation-fixture NIX-VERSION CAPTURE-JSON OUTPUT-DIR")
	}
	file, err := os.Open(args[1])
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, nixenv.MaxJSON+1))
	if err != nil {
		return err
	}
	if len(data) > nixenv.MaxJSON {
		return fmt.Errorf("capture exceeds %d bytes", nixenv.MaxJSON)
	}
	env, err := nixenv.Parse(args[0], data)
	if err != nil {
		return err
	}
	material, err := nixenv.Render(env)
	if err != nil {
		return err
	}
	packed, err := json.Marshal(material)
	if err != nil {
		return err
	}
	if _, err := nixenv.ParseMaterial(packed); err != nil {
		return err
	}
	if err := os.MkdirAll(args[2], 0755); err != nil {
		return err
	}
	for name, body := range map[string]string{
		"activation.sh": material.Script,
		"material.json": string(packed),
	} {
		if err := os.WriteFile(filepath.Join(args[2], name), []byte(body), 0644); err != nil {
			return err
		}
	}
	if material.HasStructuredAttrs {
		for name, body := range map[string]string{
			".attrs.sh":   material.AttrsSH,
			".attrs.json": material.AttrsJSON,
		} {
			if err := os.WriteFile(filepath.Join(args[2], name), []byte(body), 0644); err != nil {
				return err
			}
		}
	}
	return nil
}
