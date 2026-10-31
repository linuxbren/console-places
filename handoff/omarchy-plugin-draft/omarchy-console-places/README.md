# Console Places for Omarchy

A bar button for [Console Places](https://github.com/linuxbren/console-places), a keyboard-first
terminal file manager that follows your live Omarchy theme, with image, PDF and code previews.

Click the button to open Console Places in your terminal. Click it again to focus the window that is
already open.

## Install

This plugin launches the `console-places` program. It does not download or install that program.

1. Install Console Places and make sure `console-places` is on your `PATH`. Follow the
   [Console Places install guide](https://github.com/linuxbren/console-places#install): a release
   binary, the Arch package, or the deb/rpm/apk packages all work.

2. Add this plugin:

   ```bash
   omarchy plugin add https://github.com/linuxbren/omarchy-console-places.git --enable
   ```

If the button cannot find `console-places`, it shows a notification and does not launch anything.

## Keyboard shortcut (optional)

The plugin does not change your bindings. To open Console Places from the keyboard, add a binding
of your own that runs:

```bash
omarchy-launch-or-focus-tui --app-id=org.omarchy.console-places console-places
```

## Remove

```bash
omarchy plugin remove linuxbren.console-places
```

Removing the plugin takes the button off the bar. It does not remove the `console-places` program or
its settings.

## Dependencies

- [Console Places](https://github.com/linuxbren/console-places) (`console-places` on `PATH`)
- Omarchy, for the bar, `omarchy-launch-or-focus-tui` and `omarchy-notification-send`

## License

MIT. See [LICENSE](LICENSE).
