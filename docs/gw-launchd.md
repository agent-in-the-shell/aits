# Running aits-gw under launchd

`aits-gw serve` runs in the foreground and never detaches (P10): a supervisor
keeps it running. On macOS that is a launchd agent.

## The agent

Save this as `~/Library/LaunchAgents/aits-gw.plist`, replacing the three
`/ABSOLUTE/PATH/...` values: launchd does not expand `~` or `$HOME`. Drop the
`--inbox` pair to record only events put with `aits-gw event put`.

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>aits-gw</string>
	<key>ProgramArguments</key>
	<array>
		<string>/ABSOLUTE/PATH/TO/aits-gw</string>
		<string>serve</string>
		<string>--inbox</string>
		<string>/ABSOLUTE/PATH/TO/INBOX</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardOutPath</key>
	<string>/ABSOLUTE/PATH/TO/Library/Logs/aits-gw.log</string>
	<key>StandardErrorPath</key>
	<string>/ABSOLUTE/PATH/TO/Library/Logs/aits-gw.log</string>
</dict>
</plist>
```

- `RunAtLoad` starts serve at login. `KeepAlive` restarts it whenever it
  exits, for example with `inbox` after its directory disappeared; launchd
  waits 10s between restarts.
- The log holds serve's stderr: skipped inbox file names and the stop
  result.
- serve uses `$AITS_HOME`, default `~/.local/share/aits`, from launchd's
  `HOME`. Add `EnvironmentVariables` with `AITS_HOME` only if you use another
  root, and use the same one for `aits-gw status` and `aits-gw inbox check`.

Only one serve runs at a time. Stop any serve you started by hand before
loading the agent, or the agent's serve exits with `already_serving` until
the other one stops.

## Commands

    # load and start
    # once: launchd would create the log 0644; it keeps an existing file's mode (P7)
    install -m 600 /dev/null ~/Library/Logs/aits-gw.log
    launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/aits-gw.plist

    # check: launchd's view, then serve's
    launchctl print gui/$(id -u)/aits-gw | grep -E 'state|pid|last exit'
    aits-gw status --json                     # "serving":true
    aits-gw inbox check --inbox DIR --json    # with --inbox

    # after installing a new aits-gw binary at the same path
    launchctl kickstart -k gui/$(id -u)/aits-gw

    # stop and unload: launchd sends SIGTERM, serve logs its result
    launchctl bootout gui/$(id -u)/aits-gw
