#!/bin/bash

for try in $(seq 1 120); do
	res=$(kubectl get resourceslice -o name)
	if [ -n "${res}" ]; then
		echo "ready at try ${try}"
		# show what is ready
		kubectl get pods -A -l "app=dracpu" -o custom-columns=NAME:.metadata.name,NODE:.spec.nodeName,BUILD:.metadata.labels.build
		exit 0
	fi
	sleep 1s
done

echo "TIMEOUT: no resourceslice found after ${try} tries"
kubectl get pods -A -l "app=dracpu" -o wide
kubectl logs -n kube-system -l app=dracpu
kubectl describe daemonset -n kube-system -l app=dracpu || true
exit 1
