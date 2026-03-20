# Changelog

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
