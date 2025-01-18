# podcaster
A podcast downloader written in Go

## Features
1. Reads config file in the home directory `~/.podcaster/config.json`  to get the config about which podcasts to download, and where to store the episodes. A sample config file `sample-config.json` is included.
2. Downloads the latest configurable number of episodes of the podcasts, if not downloaded previously.
3. Remembers downloaded episodes and does not download them again even after the user has deleted the episodes. 

## Usage
```
go run main.go [-pid <podcastId>] [-count <countOfEpisodes>]
```
By default, it downloads the latest episode of all podcasts.


