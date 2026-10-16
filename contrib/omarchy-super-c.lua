-- Omarchy / Hyprland: make SUPER+C copy files in console-places.
--
-- Omarchy's "Universal copy" sends Ctrl+Insert to terminal windows, and foot
-- keeps that key for its own clipboard-copy, so console-places never sees SUPER+C.
-- Append this file to ~/.config/hypr/bindings.lua (Hyprland reloads it on save;
-- check with `hyprctl configerrors`). It only changes terminals titled
-- "console-places — …", which console-places sets itself.
--
-- SUPER+V and SUPER+X need nothing: they already reach console-places.

-- console-places: make Super+C copy files when console-places is focused.
-- Omarchy's "Universal copy" sends Ctrl+Insert to terminal windows, which foot
-- keeps for its own clipboard-copy, so console-places never sees it. A terminal whose
-- title starts with "console-places — " (set by console-places itself) gets ctrl+y, its
-- copy key, instead. Every other window behaves exactly like Omarchy's default
-- (/usr/share/omarchy/default/hypr/bindings/clipboard.lua).
local function console_places_send_once(mods, key)
  hl.dispatch(hl.dsp.send_key_state({ mods = mods, key = key, state = "down" }))
  hl.timer(function()
    hl.dispatch(hl.dsp.send_key_state({ mods = mods, key = key, state = "up" }))
  end, { timeout = 50, type = "oneshot" })
end

local function console_places_is_terminal(window)
  for _, tag in ipairs(window.tags or {}) do
    if tag:gsub("%*$", "") == "terminal" then
      return true
    end
  end
  return false
end

-- Global so it can be exercised with:
--   hyprctl dispatch '(console_places_universal_copy() or hl.dsp.exec_cmd("true"))'
function console_places_universal_copy()
  local window = hl.get_active_window()
  if window and console_places_is_terminal(window) then
    if (window.title or ""):find("console-places — ", 1, true) == 1 then
      console_places_send_once("CTRL", "Y")
    else
      console_places_send_once("CTRL", "Insert")
    end
  else
    console_places_send_once("CTRL", "C")
  end
end

-- Was: Omarchy "Universal copy" (Ctrl+Insert in terminals, Ctrl+C elsewhere).
hl.unbind("SUPER + C")
o.bind("SUPER + C", "Universal copy", console_places_universal_copy)
