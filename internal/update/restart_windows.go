package update

import "os"

// Restart starts exe with the same arguments in the same console window and
// exits this process. It only returns on failure.
func Restart(exe string, env []string) error {
	p, err := os.StartProcess(exe, os.Args, &os.ProcAttr{
		Env:   env,
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
	})
	if err != nil {
		return err
	}
	p.Release()
	os.Exit(0)
	return nil
}
