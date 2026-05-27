Unmount a local `sshfs` mount created by `ssh_mount`.

Use this when remote file work is complete. The tool uses `fusermount3`,
`fusermount`, or `umount`, whichever is available on the local machine.
