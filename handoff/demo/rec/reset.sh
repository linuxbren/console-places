H=/home/brenden; S=/workspace/console-places-review/shots
rm -rf $H/.config/console-places && cp -a /workspace/demo/config-backup $H/.config/console-places
python3 - "$1" <<'P'
import json,sys;p='/home/brenden/.config/console-places/config.json';c=json.load(open(p));c['theme']=sys.argv[1];json.dump(c,open(p,'w'),indent=2)
P
cp $S/themes/catppuccin/colors.toml $H/.local/state/omarchy/current/theme/colors.toml
echo catppuccin > $H/.local/state/omarchy/current/theme.name
