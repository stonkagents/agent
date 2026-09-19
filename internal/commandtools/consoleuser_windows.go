//go:build windows

package commandtools

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// ConsoleUser is the account logged on at the physical console (the user who
// ran the installer, or who is looking at the portal), as a SID string plus
// DOMAIN\name for the log. It reads the session's token, which needs the
// caller to be LocalSystem (SE_TCB): the MSI's deferred custom action and the
// controller service both are. The name lookup is best effort.
func ConsoleUser() (sid, name string, err error) {
	session := windows.WTSGetActiveConsoleSessionId()
	if session == 0xFFFFFFFF {
		return "", "", fmt.Errorf("no console session")
	}
	var token windows.Token
	if err := windows.WTSQueryUserToken(session, &token); err != nil {
		return "", "", fmt.Errorf("query console session %d token: %w", session, err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return "", "", fmt.Errorf("console session token user: %w", err)
	}
	sid = user.User.Sid.String()
	if account, domain, _, err := user.User.Sid.LookupAccount(""); err == nil {
		name = domain + `\` + account
	}
	return sid, name, nil
}
