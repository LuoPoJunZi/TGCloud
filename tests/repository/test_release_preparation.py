"""Offline release planning and shared CI contracts; no remote operations."""
from __future__ import annotations

import hashlib
import importlib.util
import io
import json
import re
import shutil
import sys
import tarfile
import tempfile
import unittest
from pathlib import Path
from unittest.mock import Mock, patch

from tests.fixtures import ROOT


def load(name):
    spec = importlib.util.spec_from_file_location(name, ROOT / ".github/scripts" / (name + ".py"))
    module = importlib.util.module_from_spec(spec)
    with patch.object(sys, "path", [str(ROOT / ".github/scripts"), *sys.path]):
        spec.loader.exec_module(module)
    return module


planning, validation = load("prepare_release"), load("linux_validation")
compose_release = load("compose_release")


class PlanningTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name).resolve()
        for name in ("src/tgcloud/version.py", "hostcli/internal/hostutil/version.go", "install.sh"):
            target = self.root / name
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(ROOT / name, target)
        self.current = planning.source_version(self.root)
        parts = list(map(int, self.current.split('.')))
        parts[-1] += 1
        self.next = '.'.join(map(str, parts))

    def digest(self):
        return {str(p.relative_to(self.root)): hashlib.sha256(p.read_bytes()).hexdigest()
                for p in self.root.rglob('*') if p.is_file()}

    def test_plan_does_not_change_sources_or_forge_a_helper(self):
        before = self.digest()
        report, diff = planning.prepare(self.root, self.next)
        self.assertEqual(before, self.digest())
        self.assertFalse(report["source_modified"])
        self.assertIn('+VERSION = "' + self.next + '"', diff)
        self.assertIn('+var Version = "' + self.next + '"', diff)
        self.assertNotIn('install.sh', report["mechanical_files"])
        self.assertNotIn('+installer_commit', diff)
        self.assertEqual(report["mechanical_files"], ["src/tgcloud/version.py", "hostcli/internal/hostutil/version.go"])
        self.assertEqual(len(report["release_gates"]), 4)

    def test_rejects_same_old_and_invalid_versions(self):
        for value in (self.current, '0.0.0', 'v' + self.next, '01.2.3', '1.2.3; echo secret'):
            with self.subTest(value=value), self.assertRaises(ValueError):
                planning.prepare(self.root, value)

    def test_missing_or_ambiguous_version_field_is_not_silently_skipped(self):
        path = self.root / 'install.sh'
        path.write_text('installer_version="1.0.0"\ninstaller_version="1.0.0"\n', encoding='utf-8')
        with self.assertRaises(ValueError):
            planning.prepare(self.root, self.next)

    def test_only_new_build_directory_can_receive_candidates(self):
        report, diff = planning.prepare(self.root, self.next)
        before = self.digest()
        directory = self.root / 'build/release-plan'
        planning.write_candidates(self.root, directory, report, diff)
        self.assertEqual(json.loads((directory / 'plan.json').read_text())['target'], self.next)
        self.assertEqual((directory / 'version.patch').read_text(), diff)
        self.assertTrue(all(self.digest()[name] == digest for name, digest in before.items()))
        for target in (directory, self.root, self.root / 'build', self.root / 'outside', self.root / 'build/../outside'):
            with self.subTest(target=str(target)), self.assertRaises(ValueError):
                planning.write_candidates(self.root, target, report, diff)


class ValidationTests(unittest.TestCase):
    def test_shell_stage_checks_all_entries_and_every_managed_hook(self):
        commands = validation.commands('shell')
        self.assertEqual([c for c in commands if c[:2] == ['bash', '-n']],
                         [['bash', '-n', 'install.sh'], ['bash', '-n', 'packaging/compose-https.sh']])
        self.assertIn(['shellcheck', 'install.sh', 'packaging/compose-https.sh'], commands)
        hooks = commands[-1]
        for path in (ROOT / 'packaging/hooks').glob('*/*.sh'):
            self.assertIn(path.relative_to(ROOT).as_posix(), hooks)
        self.assertIn('tests/fixtures/manage_harness.sh', hooks)

    def test_quality_preserves_both_audits_and_manifest_validation(self):
        commands = validation.commands('quality')
        self.assertEqual(len(commands), 7)
        self.assertTrue(all(c[0] == sys.executable for c in commands))
        for lock in ('requirements/bot.lock.txt', 'requirements/dev.lock.txt'):
            self.assertIn([sys.executable, '-m', 'pip_audit', '-r', lock], commands)
        self.assertTrue(any('.github/scripts/prepare_bootstrap.py' in c for c in commands))
        with self.assertRaises(ValueError):
            validation.commands('unknown')

    def test_first_failure_stops_further_commands(self):
        with patch.object(validation.subprocess, 'run', return_value=Mock(returncode=17)) as run:
            self.assertEqual(validation.main(['quality']), 17)
        run.assert_called_once()
        self.assertFalse(run.call_args.kwargs['shell'])

    def test_branch_and_release_use_current_checks_and_single_gateway(self):
        for name in ('tests.yml', 'release.yml'):
            text = (ROOT / '.github/workflows' / name).read_text(encoding='utf-8')
            for stage in ('shell', 'test', 'quality'):
                self.assertIn('.github/scripts/linux_validation.py ' + stage, text)
            # The real Compose integration consumes this local image; a
            # different tag would silently attempt to pull a nonexistent one.
            self.assertIn('docker build -t tgcloud-bot:test ', text)
        release = (ROOT / '.github/workflows/release.yml').read_text(encoding='utf-8')
        self.assertNotIn('payload_clouddrive2/', release)
        self.assertIn('--edition clouddrive2', release)
        cli = (ROOT / '.github/workflows/vps-installer.yml').read_text(encoding='utf-8')
        for name in ('.github/scripts/prepare_release.py', '.github/scripts/linux_validation.py',
                     'tests/repository/test_release_preparation.py', 'tests/deployment/proxy/test_proxy_rendering.py'):
            self.assertEqual(cli.count('      - "' + name + '"'), 2)

    def test_release_uploads_both_verified_archives_without_registry_publication(self):
        release = (ROOT / '.github/workflows/release.yml').read_text(encoding='utf-8')
        for text in ('compose_release.py bundle', 'compose_release.py verify',
                     'source_release.py verify', 'COMPOSE-SHA256SUMS.txt',
                     'TGCloud-${RELEASE_TAG}-compose.tar.gz'):
            self.assertIn(text, release)
        bundle = (ROOT / '.github/workflows/compose-bundles.yml').read_text(encoding='utf-8')
        for text in ('compose_release.py bundle', 'source_release.py verify', 'cmp "$file"'):
            self.assertIn(text, bundle)
        for text in ('docker push', 'publish-index', 'packages: write'):
            self.assertNotIn(text, bundle)

    def test_online_source_stays_pinned_to_the_documented_stable_release(self):
        online = (ROOT / 'install.sh').read_text(encoding='utf-8')
        stable_tag, = re.findall(r"^tgcloud_tag=(v" + planning.CORE + r")$", online, re.MULTILINE)
        for name in ('README.md', 'README_EN.md', 'docs/install.md'):
            text = (ROOT / name).read_text(encoding='utf-8')
            with self.subTest(document=name):
                self.assertIn('bash <(curl -fsSL https://raw.githubusercontent.com/LuoPoJunZi/TGCloud/main/install.sh)', text)
                self.assertIn(stable_tag, text)
                if name.startswith('README'):
                    self.assertIn('https://github.com/LuoPoJunZi/TGCloud/releases/tag/' + stable_tag + ')', text)
        self.assertIn('自动下载并校验 ' + stable_tag, online)
        self.assertIn('/releases/download/${tgcloud_tag}', online)


class ComposeReleaseTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        private = tempfile.TemporaryDirectory()
        cls.addClassCleanup(private.cleanup)
        cls.tag = 'v' + planning.source_version(ROOT)
        cls.original = Path(private.name).resolve() / 'release'
        cls.metadata = compose_release.build(ROOT, cls.original, cls.tag)

    def setUp(self):
        private = tempfile.TemporaryDirectory()
        self.addCleanup(private.cleanup)
        self.directory = Path(private.name).resolve() / 'assets'
        shutil.copytree(self.original, self.directory)

    def test_stable_archive_has_complete_local_source_and_required_notices(self):
        info = compose_release.verify(self.directory, self.tag)
        self.assertEqual(info, self.metadata)
        self.assertTrue(info['image'].startswith('tgcloud-local:'))
        self.assertIn('LICENSE', info['files'])
        self.assertIn('build-context/LICENSE', info['files'])
        self.assertFalse(any(name.endswith('THIRD_PARTY_NOTICES.md') for name in info['files']))
        self.assertEqual({p.name for p in self.directory.iterdir()},
                         {compose_release.artifact_name(self.tag), 'COMPOSE-SHA256SUMS.txt'})

    def test_checksum_mismatch_is_rejected(self):
        path = self.directory / compose_release.artifact_name(self.tag)
        with path.open('ab') as writer:
            writer.write(b'tampered')
        with self.assertRaisesRegex(ValueError, 'checksum'):
            compose_release.verify(self.directory, self.tag)

    def test_archive_traversal_is_rejected_even_with_matching_checksum(self):
        path = self.directory / compose_release.artifact_name(self.tag)
        with tarfile.open(path, 'w:gz') as archive:
            member = tarfile.TarInfo('bundle/../outside')
            member.size = 1
            archive.addfile(member, io.BytesIO(b'x'))
        (self.directory / 'COMPOSE-SHA256SUMS.txt').write_text(
            hashlib.sha256(path.read_bytes()).hexdigest() + '  ' + path.name + '\n', encoding='ascii')
        with self.assertRaises(ValueError):
            compose_release.verify(self.directory, self.tag)

    def test_output_and_existing_assets_are_not_overwritten(self):
        with self.assertRaises(FileExistsError):
            compose_release.build(ROOT, self.directory, self.tag)

    def test_tag_and_extra_asset_mismatches_are_rejected(self):
        for tag in ('0.0.1', 'v01.0.1', 'v0.0.1;id', 'v0.0.1-rc.0'):
            with self.subTest(tag=tag), self.assertRaises(ValueError):
                compose_release.artifact_name(tag)
        self.assertEqual(compose_release.tag_version('v0.0.1-rc.1'), '0.0.1')
        (self.directory / 'unknown.txt').write_text('unexpected', encoding='utf-8')
        with self.assertRaisesRegex(ValueError, 'asset set'):
            compose_release.verify(self.directory, self.tag)
