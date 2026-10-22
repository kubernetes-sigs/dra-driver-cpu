# Upgrade and backward compatibility

This document detail the upgrade and backward compatibility the project
should enforce for the non-user-facing surface.
See [the user upgrade notes](../user/upgrade.md) for the user-facing surface

## Backward compatibility policy

The backward compatibility promise does do cover the allocation state checkpoint,
despite this compatibility doesn't directly affect users and it's a contract between
the driver and itself.
The allocation state checkpoint data is versioned, and its version changes depending
on development needs. The driver will always read the previous version of the checkpoint
data and the current version, seamlessly. The driver will always write the most recent
supported version, converting transparently as needed.
