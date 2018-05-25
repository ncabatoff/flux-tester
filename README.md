# flux-tester
Integration tester for [flux](weaveworks/flux).

This is a translation to Go of the bash script 
[test-flux](https://github.com/weaveworks/flux/blob/master/test/bin/test-flux).

I've forked it into its own repo because I'm planning on making substatial
changes to how it works, and because I gave up trying to make the existing
script work as it's currently designed.

## Current status

The main differences with test-flux:
- Not broken (see [#919](https://github.com/weaveworks/flux/issues/919))
- By default doesn't start/delete minikube, assumes one is already running
- Requires specific minikube and k8s versions

Minor differences:
- Incorporates a proposed fix for brokenness 
  ([#921](https://github.com/weaveworks/flux/pull/921)) but with a small
  tweak: the second wait_for_sync need to look at a different revset than
  what HEAD points to, since a git fetch doesn't seem to be update that.

New functionality:
- Adds preliminary support for testing flux with the helm-operator.
  Currently TestChart gets as far as deploying a chart via git which
  flux/helm-operator turns into a helm release.  The flux agent doesn't
  report this as a controller though, so the test never succeeds.
  Relies on [#1099](https://github.com/weaveworks/flux/pull/1099)
  and [#1100](https://github.com/weaveworks/flux/pull/1100).

## Background

There's a PR ([#921](https://github.com/weaveworks/flux/pull/921)) which 
attempts to fix some of the brokenness in test-flux.  I had many issues
trying to get it to work for me, mostly relating to minikube and k8s.
- the fix relies on subpath, which is broken in k8s 1.9 prior to 1.9.5
  (see [#61076](https://github.com/kubernetes/kubernetes/issues/61076))
- minikube with the default options has various problems in recent 
  releases (0.26.0+)
- minikube has broken the -profile option in recent releases
  (see [#2717](https://github.com/kubernetes/minikube/issues/2717))
- minikube with localkube doesn't support a 1.9 release >1.9.4
- if I'm going to run minikube without localkube, I'd like to use the
  latest and greatest

What it boiled down to was that any fix I tried to make to test-flux would've
been quiet disruptive already. Moreover, I was tired of the slow feedback
loop from doing a minikube delete/start for each invocation, and I wanted to
introduce more tests (e.g. for helm), and I've written enough shell to know
when it's a good idea to stop and rewrite in a better language.