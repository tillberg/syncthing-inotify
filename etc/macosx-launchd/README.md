This directory contains an example for running Syncthing-inotify in the
background under Mac OS X.

 1. Install the `syncthing-inotify` binary in a directory called `bin` in your
    home directory.

 2. Edit the `syncthing-inotify.plist` by replacing `USERNAME` with your actual
    username such as `jb`.

 3. Copy the `syncthing-inotify.plist` file to `~/Library/LaunchAgents`.

 4. Log out and in again, or run `launchctl load ~/Library/LaunchAgents/syncthing-inotify.plist`.

Logs are in `~/Library/Logs/Syncthing-inotify.log` and, for crashes and exceptions,
`~/Library/Logs/Syncthing-inotify-errors.log`.
