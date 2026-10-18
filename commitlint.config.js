// commitlint config — extends Conventional Commits with a couple of
// project-specific tweaks so the rule set is wide enough for the kind of
// commits ZapCast actually ships (chores, deps bumps, docs) without
// fighting the contributor.

export default {
  extends: ['@commitlint/config-conventional'],
  rules: {
    // Allow longer subjects so the migration / refactor commits can name
    // the area they touch without truncating.
    'header-max-length': [2, 'always', 100],
    // The default forbids the empty body; keep it relaxed.
    'body-leading-blank': [1, 'always'],
    'footer-leading-blank': [1, 'always'],
    'type-enum': [
      2,
      'always',
      [
        'build',
        'chore',
        'ci',
        'docs',
        'feat',
        'fix',
        'perf',
        'refactor',
        'revert',
        'style',
        'test',
        'deps',
      ],
    ],
  },
};
