# Secret files

Run `./scripts/init.sh` to create the local production Secret files. The script
uses directories with mode `0750` and files with mode `0640`; the container reads
them through its dedicated runtime group.

Everything in this directory except this README is ignored by Git. Never use
`git add -f` to publish a Secret. In particular, `master-key` must be backed up
together with the SQLite volume in encrypted storage, because it decrypts the
dynamic credentials saved by the management console.
