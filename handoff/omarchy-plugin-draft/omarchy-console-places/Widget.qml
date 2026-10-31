import QtQuick
import Quickshell
import qs.Ui

BarWidget {
  id: root
  moduleName: "linuxbren.console-places"

  implicitWidth: button.implicitWidth
  implicitHeight: button.implicitHeight

  // Opens console-places in the default terminal, or focuses the window that
  // is already open. The plugin never downloads or installs anything: when the
  // console-places program is not on PATH it only shows a notification.
  function launch() {
    if (!root.bar)
      return
    Quickshell.execDetached(["bash", "-lc",
      "if command -v console-places >/dev/null; then " +
      "exec omarchy-launch-or-focus-tui --app-id=org.omarchy.console-places console-places; " +
      "else omarchy-notification-send \"Console Places\" " +
      "\"console-places is not installed. See the plugin README.\"; fi"
    ])
  }

  BarIconButton {
    id: button
    anchors.fill: parent
    bar: root.bar
    text: "󰉋"
    tooltipText: "Console Places"
    onPressed: root.launch()
  }
}
