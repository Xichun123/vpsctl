# Tech stack
- go.mod requires Go 1.26.0; module uses standard library only, system OpenSSH instead of an in-process SSH dependency.
- Local Linux/macOS; remote Linux POSIX sh/GNU coreutils. Durable jobs require procfs, setsid, nohup/base64; recursive/resumable transfer requires rsync 3+ at both ends.
