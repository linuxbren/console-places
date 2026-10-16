package main

// Remembering network passwords. gvfs looks a password up in the keyring
// (the freedesktop Secret Service: gnome-keyring, KWallet, KeePassXC…) before
// asking, but it only saves one when the program mounting asks it to, which
// the gio command never does. So when "remember" is ticked, console-places saves
// the password itself after a successful connection, in exactly the form
// gvfs (and so Nautilus) stores and finds it: the NetworkPassword schema with
// gvfs' attributes for each protocol (gvfs daemon/gvfskeyring.c and the
// backends' lookups).

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

// keyringSchemes are the protocols whose saved passwords gvfs finds again.
// WebDAV keys its entries by the server's auth realm, which only gvfs sees.
var keyringSchemes = map[string]bool{"smb": true, "sftp": true, "ftp": true, "ftps": true}

// netSecret is what to save for one connection.
type netSecret struct {
	label string
	attrs map[string]string
}

// keyringEntry builds the entry gvfs would look up for uri, with the user and
// domain that were typed at its prompts ("" if none).
func keyringEntry(uri, user, domain string) (netSecret, bool) {
	u, err := url.Parse(uri)
	if err != nil || !keyringSchemes[u.Scheme] {
		return netSecret{}, false
	}
	if user == "" && u.User != nil {
		user = u.User.Username()
	}
	if smbDomain, smbUser, ok := strings.Cut(user, ";"); ok && u.Scheme == "smb" { // smb://DOMAIN;user@host
		domain, user = smbDomain, smbUser
	}
	attrs := map[string]string{"xdg:schema": "org.gnome.keyring.NetworkPassword", "server": u.Hostname()}
	switch u.Scheme {
	case "smb":
		attrs["protocol"] = "smb"
	case "sftp":
		attrs["protocol"], attrs["authtype"] = "sftp", "password"
	default:
		attrs["protocol"] = "ftp"
	}
	if user != "" {
		attrs["user"] = user
	}
	if domain != "" && u.Scheme == "smb" {
		attrs["domain"] = domain
	}
	// The port counts only when it isn't the protocol's usual one.
	if p, err := strconv.Atoi(u.Port()); err == nil && p != 0 &&
		!(u.Scheme == "sftp" && p == 22) && !(u.Scheme == "ftp" && p == 21) && !(u.Scheme == "ftps" && p == 990) {
		attrs["port"] = strconv.Itoa(p)
	}
	label := u.Hostname()
	if user != "" {
		label = user + "@" + label
	}
	if attrs["port"] != "" {
		label += ":" + attrs["port"]
	}
	return netSecret{label: label, attrs: attrs}, true
}

const (
	secretsName = "org.freedesktop.secrets"
	secretsPath = dbus.ObjectPath("/org/freedesktop/secrets")
	secretsIfc  = "org.freedesktop.Secret"
	noPrompt    = dbus.ObjectPath("/")
)

// saveSecret stores password in the default keyring, replacing an older
// entry for the same place. A locked keyring shows its own unlock prompt.
var saveSecret = func(s netSecret, password string) error {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return errors.New("no session bus")
	}
	defer conn.Close()
	svc := conn.Object(secretsName, secretsPath)
	var out dbus.Variant
	var session dbus.ObjectPath
	if err := svc.Call(secretsIfc+".Service.OpenSession", 0, "plain", dbus.MakeVariant("")).Store(&out, &session); err != nil {
		return errors.New("no keyring (Secret Service) is running")
	}
	defer conn.Object(secretsName, session).Call(secretsIfc+".Session.Close", 0)

	coll := dbus.ObjectPath("/org/freedesktop/secrets/aliases/default")
	var unlocked []dbus.ObjectPath
	var prompt dbus.ObjectPath
	if err := svc.Call(secretsIfc+".Service.Unlock", 0, []dbus.ObjectPath{coll}).Store(&unlocked, &prompt); err == nil && prompt != noPrompt {
		if err := runPrompt(conn, prompt); err != nil {
			return err
		}
	}
	props := map[string]dbus.Variant{
		secretsIfc + ".Item.Label":      dbus.MakeVariant(s.label),
		secretsIfc + ".Item.Attributes": dbus.MakeVariant(s.attrs),
	}
	secret := struct {
		Session     dbus.ObjectPath
		Params      []byte
		Value       []byte
		ContentType string
	}{session, []byte{}, []byte(password), "text/plain; charset=utf8"}
	var item dbus.ObjectPath
	if err := conn.Object(secretsName, coll).Call(secretsIfc+".Collection.CreateItem", 0, props, secret, true).Store(&item, &prompt); err != nil {
		return errors.New("the keyring refused: " + err.Error())
	}
	if prompt != noPrompt {
		return runPrompt(conn, prompt)
	}
	return nil
}

// runPrompt shows the keyring's own prompt (unlock or confirm) and waits.
func runPrompt(conn *dbus.Conn, prompt dbus.ObjectPath) error {
	ch := make(chan *dbus.Signal, 4)
	conn.Signal(ch)
	defer conn.RemoveSignal(ch)
	if err := conn.AddMatchSignal(dbus.WithMatchObjectPath(prompt), dbus.WithMatchInterface(secretsIfc+".Prompt")); err != nil {
		return err
	}
	if err := conn.Object(secretsName, prompt).Call(secretsIfc+".Prompt.Prompt", 0, "").Err; err != nil {
		return err
	}
	timeout := time.After(2 * time.Minute)
	for {
		select {
		case sig := <-ch:
			if sig.Path != prompt || sig.Name != secretsIfc+".Prompt.Completed" {
				continue
			}
			if dismissed, _ := sig.Body[0].(bool); dismissed {
				return errors.New("the keyring stayed locked")
			}
			return nil
		case <-timeout:
			return errors.New("the keyring prompt timed out")
		}
	}
}
