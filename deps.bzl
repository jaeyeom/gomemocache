load("@bazel_gazelle//:deps.bzl", "go_repository")

def go_dependencies():
    go_repository(
        name = "com_github_jaeyeom_sugo",
        importpath = "github.com/jaeyeom/sugo",
        sum = "h1:CAvz1EglhK2pVZxMehiuYBu6fPu+rkr2ROgRnGcE6U0=", # from go.sum for the module itself
        version = "v0.0.0-20191112020940-956d7a785c73",
    )
