# gen-pcie-testdata

A tool you can run locally or in your cluster (see `Dockerfile` below) to
create a test fixture matching a machine. Only non-identifying data is
collected and the data is meant to be safe to publish.
Review is anyway always advised before publishing data.

## Dockerfile

Sometimes the machines whose topology data should be collected can be accessed
through SSH; sometimes they can be accessed through a cloud provider, sometimes both.

In case the machines are accessed through a cloud provider first and foremost,
gathering infos using containers can be simpler.
This is the reason why we add a simple but working Dockerfile alongside the code.
