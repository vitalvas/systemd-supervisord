# Changelog

## [0.2.4](https://github.com/vitalvas/systemd-supervisord/compare/v0.2.3...v0.2.4) (2026-03-20)


### Bug Fixes

* sort CLI output by name, subscribe D-Bus once, report initial health ([7ad6029](https://github.com/vitalvas/systemd-supervisord/commit/7ad6029dc2428653053df7e22acbdf4752b487db))

## [0.2.3](https://github.com/vitalvas/systemd-supervisord/compare/v0.2.2...v0.2.3) (2026-03-20)


### Bug Fixes

* report initial healthy state from health checker ([240ff9a](https://github.com/vitalvas/systemd-supervisord/commit/240ff9ae4b5f2f41d5f18ce66480afedf859a26d))

## [0.2.2](https://github.com/vitalvas/systemd-supervisord/compare/v0.2.1...v0.2.2) (2026-03-20)


### Bug Fixes

* improve config validation defaults and error messages ([ded9060](https://github.com/vitalvas/systemd-supervisord/commit/ded90602ab7ff999d333994294e1869f462c58a8))

## [0.2.1](https://github.com/vitalvas/systemd-supervisord/compare/v0.2.0...v0.2.1) (2026-03-20)


### Bug Fixes

* remove unused slog import from dbus.go ([265ec1d](https://github.com/vitalvas/systemd-supervisord/commit/265ec1dd5705fbc52d9c13a0050e1e4c2f408143))
* resolve 100% CPU usage from D-Bus subscription polling interval ([7140498](https://github.com/vitalvas/systemd-supervisord/commit/7140498db6f45c08027400a01f6438bcc2105a1a))

## [0.2.0](https://github.com/vitalvas/systemd-supervisord/compare/v0.1.0...v0.2.0) (2026-03-20)


### Features

* add dry-run mode and fix multiple daemon bugs ([87b2b09](https://github.com/vitalvas/systemd-supervisord/commit/87b2b09b550294eaaa2113131e9c1d444dbe590f))
* add priority-based unit startup ordering ([c56fecc](https://github.com/vitalvas/systemd-supervisord/commit/c56fecc9fe8b73dfdb9e949e1f49040135274665))
* add systemd socket activation support ([ebf2b6d](https://github.com/vitalvas/systemd-supervisord/commit/ebf2b6d87a85024623c3932dc8546a6789726062))
* change units config from YAML list to map keyed by name.type ([87b9279](https://github.com/vitalvas/systemd-supervisord/commit/87b92791079531a8b41a4d139d1390f88593b98b))
* improve config defaults and add instance pattern filtering ([3cc8219](https://github.com/vitalvas/systemd-supervisord/commit/3cc8219821848a1462f896f0cb71eaf6850bbf72))

## [0.1.0](https://github.com/vitalvas/systemd-supervisord/compare/v0.0.1...v0.1.0) (2026-03-20)


### Features

* add timer execution monitoring and fix CI test failures ([395da37](https://github.com/vitalvas/systemd-supervisord/commit/395da37aad94c14dd769130594c9f8710ce43a81))
* initial implementation of systemd-supervisord ([8942409](https://github.com/vitalvas/systemd-supervisord/commit/89424099a6c66c14ad0d8d01d9feb967bccc7906))
* rename dist/ to misc/, add nfpm packaging, fix CI test ([41d9373](https://github.com/vitalvas/systemd-supervisord/commit/41d93736c0e31ef2f1087e4872707cd96bb9b45f))


### Bug Fixes

* remove rpm build from goreleaser nfpm config ([11ff4bc](https://github.com/vitalvas/systemd-supervisord/commit/11ff4bc67c9917031cd2b2b8f945dc5084e64e61))
* resolve goreleaser nfpm deprecation warnings ([2af7e18](https://github.com/vitalvas/systemd-supervisord/commit/2af7e184769af8dd40aac567558c593818508706))
* set WaitDelay on script health check to prevent orphan process hang ([758892c](https://github.com/vitalvas/systemd-supervisord/commit/758892c085fb03246ee028b4d309ed0315bd77d3))
* trigger test goreleaser ([6d38aa2](https://github.com/vitalvas/systemd-supervisord/commit/6d38aa27fd80819cea11918cb4cff8f30240b84c))
