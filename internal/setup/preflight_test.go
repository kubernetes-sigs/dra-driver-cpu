/*
Copyright 2025 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Adapted from dra-driver-memory d6a6c17, pkg/sysinfo/preflight_test.go.
package setup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/require"
)

func TestValidate(t *testing.T) {
	type testcase struct {
		name          string
		mountInfo     string
		expectedError bool
	}

	testcases := []testcase{
		{
			name:          "empty mountinfo",
			mountInfo:     "",
			expectedError: true,
		},
		{
			name:          "basic with cgroup v2",
			mountInfo:     mountinfoLaptopCGroupV2,
			expectedError: false,
		},
		{
			name:          "basic without cgroup v2",
			mountInfo:     mountinfoLaptopNoCGroupV2,
			expectedError: true,
		},
		{
			name:          "basic without cgroup v2 and hugetlb acct",
			mountInfo:     mountinfoLaptopCGroupV2Acct,
			expectedError: true,
		},
	}

	for _, tcase := range testcases {
		t.Run(tcase.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "proc", "thread-self"), 0755))
			if len(tcase.mountInfo) > 0 {
				require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "proc", "thread-self", "mountinfo"), []byte(tcase.mountInfo), 0600))
			}

			logger := testr.New(t)
			err := ValidateCgroups(logger, tmpDir)
			gotErr := (err != nil)
			if gotErr != tcase.expectedError {
				t.Fatalf("got error %v expected=%v", err, tcase.expectedError)
			}
		})
	}
}

const mountinfoLaptopCGroupV2 = `74 2 MAJOR:1 / / rw,relatime shared:1 - ext4 /dev/mapper/DISK-MAIN rw,seclabel
38 74 0:6 / /dev rw,nosuid shared:2 - devtmpfs devtmpfs rw,seclabel,size=16248436k,nr_inodes=4062109,mode=755,inode64
39 38 0:26 / /dev/shm rw,nosuid,nodev shared:3 - tmpfs tmpfs rw,seclabel,inode64
40 38 0:27 / /dev/pts rw,nosuid,noexec,relatime shared:4 - devpts devpts rw,seclabel,gid=5,mode=620,ptmxmode=000
41 74 0:25 / /sys rw,nosuid,nodev,noexec,relatime shared:5 - sysfs sysfs rw,seclabel
42 41 0:7 / /sys/kernel/security rw,nosuid,nodev,noexec,relatime shared:6 - securityfs securityfs rw
43 41 0:29 / /sys/fs/cgroup rw,nosuid,nodev,noexec,relatime shared:7 - cgroup2 cgroup2 rw,seclabel,nsdelegate,memory_recursiveprot
44 41 0:30 / /sys/fs/pstore rw,nosuid,nodev,noexec,relatime shared:8 - pstore none rw,seclabel
45 41 0:31 / /sys/firmware/efi/efivars rw,nosuid,nodev,noexec,relatime shared:9 - efivarfs efivarfs rw
46 41 0:32 / /sys/fs/bpf rw,nosuid,nodev,noexec,relatime shared:10 - bpf bpf rw,mode=700
47 41 0:19 / /sys/kernel/config rw,nosuid,nodev,noexec,relatime shared:11 - configfs configfs rw
48 74 0:24 / /proc rw,nosuid,nodev,noexec,relatime shared:13 - proc proc rw
49 74 0:28 / /run rw,nosuid,nodev shared:14 - tmpfs tmpfs rw,seclabel,size=6510268k,nr_inodes=819200,mode=755,inode64
27 41 0:22 / /sys/fs/selinux rw,nosuid,noexec,relatime shared:12 - selinuxfs selinuxfs rw
26 48 0:33 / /proc/sys/fs/binfmt_misc rw,relatime shared:15 - autofs systemd-1 rw,fd=39,pgrp=1,timeout=0,minproto=5,maxproto=5,direct,pipe_ino=8651
29 41 0:8 / /sys/kernel/debug rw,nosuid,nodev,noexec,relatime shared:18 - debugfs debugfs rw,seclabel
30 41 0:13 / /sys/kernel/tracing rw,nosuid,nodev,noexec,relatime shared:17 - tracefs tracefs rw,seclabel
33 38 0:21 / /dev/mqueue rw,nosuid,nodev,noexec,relatime shared:19 - mqueue mqueue rw,seclabel
34 38 0:36 / /dev/hugepages rw,nosuid,nodev,relatime shared:20 - hugetlbfs hugetlbfs rw,seclabel,pagesize=2M
95 49 0:37 / /run/credentials/systemd-journald.service ro,nosuid,nodev,noexec,relatime,nosymfollow shared:21 - tmpfs tmpfs rw,seclabel,size=1024k,nr_inodes=1024,mode=700,inode64,noswap
50 41 0:39 / /sys/fs/fuse/connections rw,nosuid,nodev,noexec,relatime shared:72 - fusectl fusectl rw
54 74 MAJOR:2 / /boot rw,relatime shared:75 - ext4 /dev/nvme0n1p2 rw,seclabel
57 74 MAJOR:4 / /var rw,relatime shared:78 - ext4 /dev/mapper/DISK-VAR rw,seclabel
60 54 MAJOR:1 / /boot/efi rw,relatime shared:81 - vfat /dev/BOOT rw,fmask=0077,dmask=0077,codepage=437,iocharset=ascii,shortname=winnt,errors=remount-ro
63 74 MAJOR:3 / /tmp rw,relatime shared:84 - ext4 /dev/mapper/DISK-TEMP rw,seclabel
64 74 MAJOR:5 / /home rw,relatime shared:87 - ext4 /dev/mapper/DISK-HOME rw,seclabel
209 49 0:43 / /run/credentials/systemd-resolved.service ro,nosuid,nodev,noexec,relatime,nosymfollow shared:90 - tmpfs tmpfs rw,seclabel,size=1024k,nr_inodes=1024,mode=700,inode64,noswap
217 26 0:46 / /proc/sys/fs/binfmt_misc rw,nosuid,nodev,noexec,relatime shared:93 - binfmt_misc binfmt_misc rw
71 57 0:48 / /var/lib/nfs/rpc_pipefs rw,relatime shared:157 - rpc_pipefs sunrpc rw
28 49 0:83 / /run/user/1000 rw,nosuid,nodev,relatime shared:1181 - tmpfs tmpfs rw,seclabel,size=3255132k,nr_inodes=813783,mode=700,uid=1000,gid=1000,inode64
1226 28 0:84 / /run/user/1000/gvfs rw,nosuid,nodev,relatime shared:1213 - fuse.gvfsd-fuse gvfsd-fuse rw,user_id=1000,group_id=1000
1326 28 0:85 / /run/user/1000/doc rw,nosuid,nodev,relatime shared:1245 - fuse.portal portal rw,user_id=1000,group_id=1000`

const mountinfoLaptopCGroupV2Acct = `74 2 MAJOR:1 / / rw,relatime shared:1 - ext4 /dev/mapper/DISK-MAIN rw,seclabel
38 74 0:6 / /dev rw,nosuid shared:2 - devtmpfs devtmpfs rw,seclabel,size=16248436k,nr_inodes=4062109,mode=755,inode64
39 38 0:26 / /dev/shm rw,nosuid,nodev shared:3 - tmpfs tmpfs rw,seclabel,inode64
40 38 0:27 / /dev/pts rw,nosuid,noexec,relatime shared:4 - devpts devpts rw,seclabel,gid=5,mode=620,ptmxmode=000
41 74 0:25 / /sys rw,nosuid,nodev,noexec,relatime shared:5 - sysfs sysfs rw,seclabel
42 41 0:7 / /sys/kernel/security rw,nosuid,nodev,noexec,relatime shared:6 - securityfs securityfs rw
43 41 0:29 / /sys/fs/cgroup rw,nosuid,nodev,noexec,relatime,memory_hugetlb_accounting shared:7 - cgroup2 cgroup2 rw,seclabel,nsdelegate,memory_recursiveprot
44 41 0:30 / /sys/fs/pstore rw,nosuid,nodev,noexec,relatime shared:8 - pstore none rw,seclabel
45 41 0:31 / /sys/firmware/efi/efivars rw,nosuid,nodev,noexec,relatime shared:9 - efivarfs efivarfs rw
46 41 0:32 / /sys/fs/bpf rw,nosuid,nodev,noexec,relatime shared:10 - bpf bpf rw,mode=700
47 41 0:19 / /sys/kernel/config rw,nosuid,nodev,noexec,relatime shared:11 - configfs configfs rw
48 74 0:24 / /proc rw,nosuid,nodev,noexec,relatime shared:13 - proc proc rw
49 74 0:28 / /run rw,nosuid,nodev shared:14 - tmpfs tmpfs rw,seclabel,size=6510268k,nr_inodes=819200,mode=755,inode64
27 41 0:22 / /sys/fs/selinux rw,nosuid,noexec,relatime shared:12 - selinuxfs selinuxfs rw
26 48 0:33 / /proc/sys/fs/binfmt_misc rw,relatime shared:15 - autofs systemd-1 rw,fd=39,pgrp=1,timeout=0,minproto=5,maxproto=5,direct,pipe_ino=8651
29 41 0:8 / /sys/kernel/debug rw,nosuid,nodev,noexec,relatime shared:18 - debugfs debugfs rw,seclabel
30 41 0:13 / /sys/kernel/tracing rw,nosuid,nodev,noexec,relatime shared:17 - tracefs tracefs rw,seclabel
33 38 0:21 / /dev/mqueue rw,nosuid,nodev,noexec,relatime shared:19 - mqueue mqueue rw,seclabel
34 38 0:36 / /dev/hugepages rw,nosuid,nodev,relatime shared:20 - hugetlbfs hugetlbfs rw,seclabel,pagesize=2M
95 49 0:37 / /run/credentials/systemd-journald.service ro,nosuid,nodev,noexec,relatime,nosymfollow shared:21 - tmpfs tmpfs rw,seclabel,size=1024k,nr_inodes=1024,mode=700,inode64,noswap
50 41 0:39 / /sys/fs/fuse/connections rw,nosuid,nodev,noexec,relatime shared:72 - fusectl fusectl rw
54 74 MAJOR:2 / /boot rw,relatime shared:75 - ext4 /dev/nvme0n1p2 rw,seclabel
57 74 MAJOR:4 / /var rw,relatime shared:78 - ext4 /dev/mapper/DISK-VAR rw,seclabel
60 54 MAJOR:1 / /boot/efi rw,relatime shared:81 - vfat /dev/BOOT rw,fmask=0077,dmask=0077,codepage=437,iocharset=ascii,shortname=winnt,errors=remount-ro
63 74 MAJOR:3 / /tmp rw,relatime shared:84 - ext4 /dev/mapper/DISK-TEMP rw,seclabel
64 74 MAJOR:5 / /home rw,relatime shared:87 - ext4 /dev/mapper/DISK-HOME rw,seclabel
209 49 0:43 / /run/credentials/systemd-resolved.service ro,nosuid,nodev,noexec,relatime,nosymfollow shared:90 - tmpfs tmpfs rw,seclabel,size=1024k,nr_inodes=1024,mode=700,inode64,noswap
217 26 0:46 / /proc/sys/fs/binfmt_misc rw,nosuid,nodev,noexec,relatime shared:93 - binfmt_misc binfmt_misc rw
71 57 0:48 / /var/lib/nfs/rpc_pipefs rw,relatime shared:157 - rpc_pipefs sunrpc rw
28 49 0:83 / /run/user/1000 rw,nosuid,nodev,relatime shared:1181 - tmpfs tmpfs rw,seclabel,size=3255132k,nr_inodes=813783,mode=700,uid=1000,gid=1000,inode64
1226 28 0:84 / /run/user/1000/gvfs rw,nosuid,nodev,relatime shared:1213 - fuse.gvfsd-fuse gvfsd-fuse rw,user_id=1000,group_id=1000
1326 28 0:85 / /run/user/1000/doc rw,nosuid,nodev,relatime shared:1245 - fuse.portal portal rw,user_id=1000,group_id=1000`

const mountinfoLaptopNoCGroupV2 = `74 2 MAJOR:1 / / rw,relatime shared:1 - ext4 /dev/mapper/DISK-MAIN rw,seclabel
38 74 0:6 / /dev rw,nosuid shared:2 - devtmpfs devtmpfs rw,seclabel,size=16248436k,nr_inodes=4062109,mode=755,inode64
39 38 0:26 / /dev/shm rw,nosuid,nodev shared:3 - tmpfs tmpfs rw,seclabel,inode64
40 38 0:27 / /dev/pts rw,nosuid,noexec,relatime shared:4 - devpts devpts rw,seclabel,gid=5,mode=620,ptmxmode=000
41 74 0:25 / /sys rw,nosuid,nodev,noexec,relatime shared:5 - sysfs sysfs rw,seclabel
42 41 0:7 / /sys/kernel/security rw,nosuid,nodev,noexec,relatime shared:6 - securityfs securityfs rw
44 41 0:30 / /sys/fs/pstore rw,nosuid,nodev,noexec,relatime shared:8 - pstore none rw,seclabel
45 41 0:31 / /sys/firmware/efi/efivars rw,nosuid,nodev,noexec,relatime shared:9 - efivarfs efivarfs rw
46 41 0:32 / /sys/fs/bpf rw,nosuid,nodev,noexec,relatime shared:10 - bpf bpf rw,mode=700
47 41 0:19 / /sys/kernel/config rw,nosuid,nodev,noexec,relatime shared:11 - configfs configfs rw
48 74 0:24 / /proc rw,nosuid,nodev,noexec,relatime shared:13 - proc proc rw
49 74 0:28 / /run rw,nosuid,nodev shared:14 - tmpfs tmpfs rw,seclabel,size=6510268k,nr_inodes=819200,mode=755,inode64
27 41 0:22 / /sys/fs/selinux rw,nosuid,noexec,relatime shared:12 - selinuxfs selinuxfs rw
26 48 0:33 / /proc/sys/fs/binfmt_misc rw,relatime shared:15 - autofs systemd-1 rw,fd=39,pgrp=1,timeout=0,minproto=5,maxproto=5,direct,pipe_ino=8651
29 41 0:8 / /sys/kernel/debug rw,nosuid,nodev,noexec,relatime shared:18 - debugfs debugfs rw,seclabel
30 41 0:13 / /sys/kernel/tracing rw,nosuid,nodev,noexec,relatime shared:17 - tracefs tracefs rw,seclabel
33 38 0:21 / /dev/mqueue rw,nosuid,nodev,noexec,relatime shared:19 - mqueue mqueue rw,seclabel
34 38 0:36 / /dev/hugepages rw,nosuid,nodev,relatime shared:20 - hugetlbfs hugetlbfs rw,seclabel,pagesize=2M
95 49 0:37 / /run/credentials/systemd-journald.service ro,nosuid,nodev,noexec,relatime,nosymfollow shared:21 - tmpfs tmpfs rw,seclabel,size=1024k,nr_inodes=1024,mode=700,inode64,noswap
50 41 0:39 / /sys/fs/fuse/connections rw,nosuid,nodev,noexec,relatime shared:72 - fusectl fusectl rw
54 74 MAJOR:2 / /boot rw,relatime shared:75 - ext4 /dev/nvme0n1p2 rw,seclabel
57 74 MAJOR:4 / /var rw,relatime shared:78 - ext4 /dev/mapper/DISK-VAR rw,seclabel
60 54 MAJOR:1 / /boot/efi rw,relatime shared:81 - vfat /dev/BOOT rw,fmask=0077,dmask=0077,codepage=437,iocharset=ascii,shortname=winnt,errors=remount-ro
63 74 MAJOR:3 / /tmp rw,relatime shared:84 - ext4 /dev/mapper/DISK-TEMP rw,seclabel
64 74 MAJOR:5 / /home rw,relatime shared:87 - ext4 /dev/mapper/DISK-HOME rw,seclabel
209 49 0:43 / /run/credentials/systemd-resolved.service ro,nosuid,nodev,noexec,relatime,nosymfollow shared:90 - tmpfs tmpfs rw,seclabel,size=1024k,nr_inodes=1024,mode=700,inode64,noswap
217 26 0:46 / /proc/sys/fs/binfmt_misc rw,nosuid,nodev,noexec,relatime shared:93 - binfmt_misc binfmt_misc rw
71 57 0:48 / /var/lib/nfs/rpc_pipefs rw,relatime shared:157 - rpc_pipefs sunrpc rw
28 49 0:83 / /run/user/1000 rw,nosuid,nodev,relatime shared:1181 - tmpfs tmpfs rw,seclabel,size=3255132k,nr_inodes=813783,mode=700,uid=1000,gid=1000,inode64
1226 28 0:84 / /run/user/1000/gvfs rw,nosuid,nodev,relatime shared:1213 - fuse.gvfsd-fuse gvfsd-fuse rw,user_id=1000,group_id=1000
1326 28 0:85 / /run/user/1000/doc rw,nosuid,nodev,relatime shared:1245 - fuse.portal portal rw,user_id=1000,group_id=1000`
