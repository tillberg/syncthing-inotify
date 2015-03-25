#### What is this?
Syncthing ([core](https://github.com/syncthing/syncthing)) uses a rescan interval to detect changes in folders. This application (syncthing-inotify) uses OS primitives to detect changes as soon as they happen. Therefore, if you save a file, syncthing-inotify will know about it and pass this information to Syncthing such that near real-time synchronisation can be achieved.

#### Where do I get it?
Syncthing-inotify binaries are released on [github](https://github.com/syncthing/syncthing-inotify/releases/latest)

#### How do I run it?
Syncthing-inotify will automatically read the Syncthing config if it is found in the standard path.
  * Run and hide on unix in a screen
```
screen -S inotify -dm ./syncthing-inotify
```
  * Run and hide on windows in .vbs script
```
CreateObject("Wscript.Shell").Run "syncthing-inotify.exe, 0, True
```
  * Run and hide on windows using API key in .vbs script
```
CreateObject("Wscript.Shell").Run "syncthing-inotify.exe -api=""...""", 0, True
```
  * Install as a service, see the etc/ folder

#### I'm confused
  * Try [Syncthing-GTK](https://github.com/syncthing/syncthing-gtk)
  * Read the commandline options: ```./syncthing-inotify -help```. Settings, such as an API key, need to be manually provided if you use a custom home for Syncthing.

#### Building syncthing-inotify
```
# To clone
mkdir -p src/github.com/syncthing
git clone https://github.com/syncthing/syncthing-inotify.git src/github.com/syncthing/syncthing-inotify
# Following commands are needed every time you want to build (unless you use Golang's specific folder structure: C:\src  or ~/src/)
export GOPATH=$(pwd)
cd src/github.com/syncthing/syncthing-inotify
go get
go build
```


#### Troubleshooting (OSX)
* The Go bindings for inotify do not support recursive watching on OSX. Therefore, when watching many files on OSX, we might not have enough inotify handles available and the app exits with the message:```no space left on device```. This is an [open issue](https://github.com/syncthing/syncthing-inotify/issues/8) and [common for other applications as well ](http://superuser.com/a/443168). Linux might also be affected by this issue when you have many subdirectories.

  Temporary fix for OSX: ```sudo sh -c 'echo kern.maxfiles=20480\\nkern.maxfilesperproc=18000 >> /etc/sysctl.conf'```

  Temporary fix for Linux: ```sudo sh -c 'echo fs.inotify.max_user_watches=20480\n >> /etc/sysctl.conf'```
