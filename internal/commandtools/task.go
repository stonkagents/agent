package commandtools

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/stonkagents/agent/internal/installenv"
)

// Runner runs one command and returns its combined output, like
// setup.CommandRunner. Tests substitute a fake.
type Runner func(name string, args ...string) ([]byte, error)

// TaskName is the scheduled task of an environment: "StonkAgents command
// tools", "StonkAgents Dev command tools", "StonkAgents Staging command tools".
func TaskName(p installenv.Profile) string {
	return p.ProductName + " command tools"
}

// ToolExe is the background job's executable inside an install folder. An
// explicit backslash join: the path is a Windows one even when the caller is
// unit-tested on another OS.
func ToolExe(installDir string) string {
	return strings.TrimRight(installDir, `\/`) + `\stonkagents-tools.exe`
}

// parentDir is the folder of a Windows path, whichever separator it uses.
func parentDir(path string) string {
	if i := strings.LastIndexAny(path, `\/`); i >= 0 {
		return path[:i]
	}
	return path
}

// ToolArgs are the job's arguments for an environment and data folder. A
// trailing separator (the MSI's [DATADIR] ends with one) is dropped: a
// backslash right before the closing quote would escape it on the command line.
func ToolArgs(env, dataDir string) string {
	return fmt.Sprintf(`--env %s --background --data-dir "%s"`, env, strings.TrimRight(dataDir, `\/`))
}

// TaskXML is the Task Scheduler definition the task is registered from
// (schtasks /Create /XML), which is locale independent where /SC ONCE /ST /SD
// are not. userID is the account the task runs as: a SID (preferred; what the
// installer resolves for the console session) or DOMAIN\user.
//
//   - LogonType InteractiveToken: the job runs on the user's desktop with the
//     user's own token and needs no stored password, which is what lets a
//     LocalSystem process (the MSI's deferred custom action, the controller
//     service) register it and start it. It runs only while that user is
//     logged on, which is the case right after an interactive install and
//     whenever the portal asks for a retry.
//   - RunLevel HighestAvailable: an administrator's full token, as the MSI's
//     impersonated custom action had; a standard user's token as is.
//   - No triggers: the task only runs on demand (schtasks /Run), right after
//     registration and on every retry. The registration is kept so a retry
//     needs no elevation and the uninstaller can delete it.
//   - ExecutionTimeLimit two hours: the job's own timeouts add up to well
//     under that; the limit only ends a job that hung anyway.
func TaskXML(exe, args, userID string) string {
	esc := func(s string) string {
		var b strings.Builder
		_ = xml.EscapeText(&b, []byte(s))
		return b.String()
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<Task version="1.4" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>Installs the StonkAgents command tools (npm CLI, onboarding, OpenClaw gateway) in the background after setup. Progress shows in the StonkAgents portal.</Description>
  </RegistrationInfo>
  <Triggers />
  <Principals>
    <Principal id="Author">
      <UserId>` + esc(userID) + `</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>HighestAvailable</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>true</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <IdleSettings>
      <StopOnIdleEnd>false</StopOnIdleEnd>
      <RestartOnIdle>false</RestartOnIdle>
    </IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <WakeToRun>false</WakeToRun>
    <ExecutionTimeLimit>PT2H</ExecutionTimeLimit>
    <Priority>7</Priority>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>` + esc(exe) + `</Command>
      <Arguments>` + esc(args) + `</Arguments>
      <WorkingDirectory>` + esc(parentDir(exe)) + `</WorkingDirectory>
    </Exec>
  </Actions>
</Task>
`
}

// schtasks is the full path of schtasks.exe: a LocalSystem caller (the MSI
// custom action, the controller service) has a minimal PATH.
func schtasks() string {
	if root := os.Getenv("SystemRoot"); root != "" {
		return filepath.Join(root, "System32", "schtasks.exe")
	}
	return "schtasks.exe"
}

// Register creates or replaces the task from TaskXML. The XML is written to a
// temp file for schtasks (UTF-8, as its declaration says) and removed afterwards.
func Register(run Runner, name, exe, args, userID string) error {
	dir, err := os.MkdirTemp("", "stonkagents-task-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, "task.xml")
	if err := os.WriteFile(file, []byte(TaskXML(exe, args, userID)), 0o600); err != nil {
		return err
	}
	out, err := run(schtasks(), "/Create", "/F", "/TN", name, "/XML", file)
	if err != nil {
		return fmt.Errorf("schtasks /Create %q: %v: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Run starts the task now. With an InteractiveToken principal the job appears
// in that user's session; when the user is not logged on schtasks fails and
// the error says so.
func Run(run Runner, name string) error {
	out, err := run(schtasks(), "/Run", "/TN", name)
	if err != nil {
		return fmt.Errorf("schtasks /Run %q: %v: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Exists reports whether the task is registered.
func Exists(run Runner, name string) bool {
	_, err := run(schtasks(), "/Query", "/TN", name)
	return err == nil
}

// Delete ends a running instance and removes the task. A task that does not
// exist is not an error.
func Delete(run Runner, name string) error {
	if !Exists(run, name) {
		return nil
	}
	_, _ = run(schtasks(), "/End", "/TN", name)
	out, err := run(schtasks(), "/Delete", "/F", "/TN", name)
	if err != nil {
		return fmt.Errorf("schtasks /Delete %q: %v: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}
