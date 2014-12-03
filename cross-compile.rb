#!/usr/bin/env ruby
oses = [
  "linux-386", "linux-amd64", "linux-arm-v5", "linux-arm-v7",
  "darwin-386", "darwin-amd64",
  "openbsd-386", "openbsd-amd64",
  "freebsd-386", "freebsd-amd64", "freebsd-arm-v5", "freebsd-arm-v7",
  "windows-386", "windows-amd64"]

version = `git describe --abbrev=0 --tags`.chomp

oses.each do |os|
  name = "syncthing-inotify"
  newname = "syncthing-inotify-#{os}"
  buildX = ""
  cross = os.gsub(/-v\d/,"")
  if os.include?("arm")
    buildX = "GOARM=#{os[-1]}"
  end
  if os.include?("windows")
    name = name + ".exe"
  end
  build = "#{buildX} go-#{cross} build"
  package = "tar -czf syncthing-inotify-#{os}-#{version}.tar.gz #{name}"
  rename = "mv #{name} #{newname}"
  `/bin/bash -ic 'source ../../davecheney/golang-crosscompile/crosscompile.bash && #{build} && #{package} && #{rename}'`
end
