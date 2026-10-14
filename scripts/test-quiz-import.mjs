import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, rm, readFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createRequire } from 'node:module';
import { buildSync } from 'esbuild';

test('AI JSON creates only editable drafts and validates range answers and winner rules', async () => {
 const dir = await mkdtemp(join(tmpdir(), 'quiz-import-'));
 try {
  const file = join(dir, 'quiz.cjs');
  buildSync({ stdin: { contents: `export { parseQuizImport, localizeQuiz, localizeQuizQuestion } from './quiz';`, resolveDir: new URL('../src/lib/', import.meta.url).pathname }, bundle: true, platform: 'node', format: 'cjs', outfile: file, define: { 'import.meta.env.BASE_URL': '"/"', 'import.meta.env': '{}' }, logLevel: 'silent' });
  const { parseQuizImport, localizeQuiz, localizeQuizQuestion } = createRequire(import.meta.url)(file);
  const raw = { title: 'Closest', prize: { title: 'Manual reward' }, winner_count: 3, questions: [{ type: 'range', prompt: 'Guess a number', min: 0, max: 1000, correct_value: 500 }, { prompt: 'Pick', options: ['A','B'], correct_index: 1 }], id: 'foreign', status: 'published', winner_user_ids: ['attacker'], participants: 100 };
  const result = parseQuizImport(JSON.stringify(raw));
  const example = parseQuizImport(await readFile(new URL('../docs/examples/shop-knowledge-quiz.json', import.meta.url), 'utf8'));
  assert.equal(example.questions.length, 10);
  assert.equal(example.winner_count, 5); assert.equal(example.tie_break, 'submitted_at');
  assert.equal(example.prize.value, 'ILOVENIM');
  assert.equal(example.payout.enabled,false);
  assert.deepEqual(example.cashback_reward,{code:'ILOVENIM',code_mode:'shared',cashback_bps:100,max_recipients:0,min_score_percent:0});
  assert.equal(Date.parse(example.ends_at),Date.parse('2026-11-11T09:00:00Z'));
  for(const payout of [{enabled:true,source:'other',pool_nim:10000,per_winner_nim:2000},{enabled:true,source:'cashback_wallet',pool_nim:1000,per_winner_nim:2000},{enabled:true,source:'cashback_wallet',pool_nim:10000,per_winner_nim:2000.5},{enabled:'true',source:'cashback_wallet',pool_nim:10000,per_winner_nim:2000}]) assert.throws(()=>parseQuizImport(JSON.stringify({...raw,tie_break:'submitted_at',payout})));
  assert.throws(()=>parseQuizImport(JSON.stringify({...raw,winner_count:0,tie_break:'submitted_at',payout:{enabled:true,source:'cashback_wallet',pool_nim:10000,per_winner_nim:2000}})));
  assert.throws(()=>parseQuizImport(JSON.stringify({...raw,tie_break:'',payout:{enabled:true,source:'cashback_wallet',pool_nim:10000,per_winner_nim:2000}})));

  assert.throws(() => parseQuizImport(JSON.stringify({ ...raw, tie_break: 'client_time' })));
  for (const lang of ['en','tr','de','fr','es','pt']) {
   assert.ok(localizeQuiz(example, lang).title);
   assert.equal(localizeQuiz(example, lang).description, example.translations[lang].description);
   assert.equal(localizeQuiz(example, lang).prize.title, example.prize.translations[lang].title);
   for (const question of example.questions) {
    assert.equal(localizeQuizQuestion(question, lang).prompt, question.translations[lang].prompt);
    assert.equal(localizeQuizQuestion(question, lang).correct_value, question.correct_value);
   }
  }
  assert.equal(example.questions.filter(q => q.type === 'choice').length, 9);
  assert.equal(example.questions.filter(q => q.type === 'range').length, 1);
  assert.equal(example.questions[0].options[example.questions[0].correct_index], '100 NIM');
  for (const question of example.questions.filter(q => q.type === 'choice')) {
   assert.equal(question.correct_value, undefined);
   for (const lang of ['en','tr','de','fr','es','pt']) {
    assert.equal(question.translations[lang].options.length, question.options.length);
    assert.deepEqual(localizeQuizQuestion(question, lang).options, question.translations[lang].options);
   }
  }
  assert.equal(example.questions[1].correct_value, 20);
  assert.equal(localizeQuizQuestion(result.questions[0], 'fr').prompt, result.questions[0].prompt);
  assert.throws(() => parseQuizImport(JSON.stringify({ ...raw, translations: { xx: { title: 'bad' } } })));
  assert.throws(() => parseQuizImport(JSON.stringify({ ...raw, questions: [{ ...raw.questions[0], translations: { en: { prompt: 'bad', options: ['secret'] } } }] })));
  assert.equal(result.status, 'draft'); assert.equal(result.id, undefined); assert.equal(result.winner_user_ids, undefined); assert.equal(result.participants, undefined);
  assert.equal(result.winner_count, 3); assert.equal(result.questions[0].min, 0); assert.equal(result.questions[0].correct_value, 500); assert.deepEqual(result.questions[0].options, []);
  assert.notEqual(result.questions[0].id, result.questions[1].id); assert.ok(Date.parse(result.ends_at) > Date.parse(result.starts_at));
  assert.equal(parseQuizImport(JSON.stringify({ ...raw, winner_count: 0 })).winner_count, 0);
  for (const question of [{ type: 'range', prompt: 'Missing', min: 0, max: 1000 }, { type: 'range', prompt: 'Outside', min: 1, max: 1000, correct_value: 1001 }, { type: 'range', prompt: 'Decimal', min: 0, max: 10, correct_value: 1.5 }, { type: 'range', prompt: 'Reversed', min: 10, max: 1, correct_value: 5 }, { prompt: 'Duplicate', options: ['A','a'], correct_index: 0 }, { prompt: 'Wrong index', options: ['A','B'], correct_index: 2 }]) assert.throws(() => parseQuizImport(JSON.stringify({ ...raw, questions: [question] })));
  assert.throws(() => parseQuizImport(JSON.stringify({ ...raw, winner_count: -1 })));
  assert.throws(() => parseQuizImport(JSON.stringify({ ...raw, starts_at: '2026-10-12T10:00', ends_at: '2026-10-12T12:00' })));
  assert.throws(() => parseQuizImport(JSON.stringify({ ...raw, prize: { title: 'Prize', image_url: 'javascript:alert(1)' } })));
  assert.throws(() => parseQuizImport('x'.repeat(256 * 1024 + 1)));
  assert.throws(() => parseQuizImport('```json\n{}\n```'));
 } finally { await rm(dir, { recursive: true, force: true }); }
});
