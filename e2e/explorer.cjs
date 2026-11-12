// End-to-end test of the explorer. It starts `moonwalk serve` on copies of
// the test fixtures, drives the page in headless Chromium, and exits with a
// failure if anything is not as expected.
//
//   go build -o moonwalk ./cmd/moonwalk
//   MOONWALK_BIN=./moonwalk node e2e/explorer.cjs

const { chromium } = require('playwright');
const { spawn } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const bin = path.resolve(process.env.MOONWALK_BIN || './moonwalk');
const fixtures = path.resolve(__dirname, '../internal/tfstate/testdata');
const port = 20000 + Math.floor(Math.random() * 20000);
const base = `http://127.0.0.1:${port}/`;

let failures = 0;
function check(name, ok, detail) {
  if (ok) {
    console.log('ok   ' + name);
  } else {
    failures++;
    console.log('FAIL ' + name + (detail === undefined ? '' : ': ' + JSON.stringify(detail)));
  }
}

// A state of six blocks with 90 instances each, for the item budget.
function crowdedState() {
  const resources = [];
  for (const name of ['a', 'b', 'c', 'd', 'e', 'f']) {
    const instances = [];
    for (let i = 0; i < 90; i++) instances.push({ index_key: i, attributes: { id: `${name}-${i}` }, sensitive_attributes: [] });
    resources.push({ mode: 'managed', type: 'test_thing', name, provider: 'provider["example.com/test/test"]', instances });
  }
  return JSON.stringify({ version: 4, terraform_version: '1.9.5', resources });
}

async function classOfItem(page, text) {
  return (await page.locator('.item', { hasText: text }).first().getAttribute('class')) || '';
}

async function waitForServer() {
  for (let i = 0; i < 100; i++) {
    try {
      if ((await fetch(base + 'api/meta')).ok) return;
    } catch {}
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error('the server did not start');
}

async function main() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'moonwalk-e2e-'));
  const net = path.join(dir, 'net.tfstate');
  fs.copyFileSync(path.join(fixtures, 'network.tfstate'), net);
  fs.copyFileSync(path.join(fixtures, 'secrets.tfstate'), path.join(dir, 'lab.tfstate'));
  fs.copyFileSync(path.join(fixtures, 'unmarked.tfstate'), path.join(dir, 'old.tfstate'));
  fs.writeFileSync(path.join(dir, 'crowd.tfstate'), crowdedState());

  const code = path.resolve(__dirname, '../internal/tfcode/testdata/network');
  const planCode = path.resolve(__dirname, '../internal/tfcode/testdata/plan');
  fs.copyFileSync(path.join(fixtures, 'plan.json'), path.join(dir, 'plan.json'));
  const found = path.resolve(__dirname, '../internal/discover/testdata');
  fs.copyFileSync(path.join(fixtures, 'drift.json'), path.join(dir, 'drift.json'));
  const server = spawn(bin, ['serve', '-addr', `127.0.0.1:${port}`, '-code', `net=${code}`, '-code', `plan=${planCode}`, `plan=${path.join(dir, 'plan.json')}`,
    `prod=${path.join(found, 'managed.tfstate')}`, `cloud=${path.join(found, 'query.jsonl')}`, `drift=${path.join(dir, 'drift.json')}`,
    `net=${net}`, `lab=${path.join(dir, 'lab.tfstate')}`, `old=${path.join(dir, 'old.tfstate')}`, `crowd=${path.join(dir, 'crowd.tfstate')}`,
    `az=${path.join(fixtures, 'azure.tfstate')}`],
  { stdio: ['ignore', 'inherit', 'inherit'] });
  const browser = await chromium.launch();
  try {
    await waitForServer();
    const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
    const errors = [];
    page.on('pageerror', (e) => errors.push(String(e)));
    page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()); });
    const item = (text) => page.locator('.item', { hasText: text }).first();
    const resources = () => page.locator('.item.resource').allInnerTexts();
    const settled = () => page.waitForLoadState('networkidle');

    // The first view, and a resource's details.
    await page.goto(base + '#a=123456789012');
    await page.waitForSelector('.item');
    check('account filter from the address bar', (await page.inputValue('#account')) === '123456789012');
    check('filtered to the one account', (await page.innerText('#summary')).includes('18 resources'), await page.innerText('#summary'));
    await item('aws_instance.app').click();
    await page.waitForSelector('#panel h2');
    const panel = await page.innerText('#panel');
    check('panel names the resource', panel.includes('aws_instance.app') && panel.includes('us-east-1'));
    check('panel lists what it depends on', panel.includes('Depends on (3)') && panel.includes('aws_subnet.private[1]'), panel);
    check('panel shows where the resource is declared', panel.includes('main.tf, line 29') && (await page.locator('#panel pre.code').innerText()).startsWith('resource "aws_instance" "app" {'));

    // A resource that is in the code but not the state is drawn as such.
    const declared = item('aws_security_group.app');
    check('a resource only in code is marked declared', (await declared.getAttribute('class')).includes('declared'));
    await declared.click();
    await page.waitForFunction(() => document.querySelector('#panel h2')?.textContent === 'aws_security_group.app');
    check('its panel says it is not applied yet', (await page.innerText('#panel')).includes('not applied yet'));
    await item('aws_instance.app').click();
    await page.waitForFunction(() => document.querySelector('#panel h2')?.textContent === 'aws_instance.app');

    // Following a reference selects it.
    await page.locator('#panel .ref', { hasText: 'aws_subnet.private[1]' }).first().click();
    await page.waitForFunction(() => document.querySelector('#panel h2')?.textContent === 'aws_subnet.private[1]');
    check('following a reference selects it', (await page.locator('.item.selected').innerText()).includes('aws_subnet.private[1]'));

    // A link lists the edges behind it.
    await page.keyboard.press('Escape');
    await page.locator('#links path.hit').first().dispatchEvent('click');
    await page.waitForFunction(() => /→|—/.test(document.querySelector('#panel h2')?.textContent || ''));
    check('a link lists its edges', (await page.locator('#panel li.pair').count()) > 0, await page.innerText('#panel'));
    await page.keyboard.press('Escape');
    check('Escape clears the panel', (await page.innerText('#panel')).startsWith('Choose a resource'));

    // Filters and grouping.
    await page.selectOption('#region', 'global');
    await page.waitForFunction(() => document.querySelectorAll('.item.resource').length === 2);
    check('region filter', (await resources()).join() === 'aws_iam_policy.read,aws_iam_role.app', await resources());
    await page.selectOption('#region', '');
    await page.waitForFunction(() => document.querySelectorAll('.item.resource').length === 18);
    await page.selectOption('#group-by', 'source,account,region,network');
    await page.waitForFunction(() => [...document.querySelectorAll('.title small')].some((e) => e.textContent.startsWith('network')));
    check('network grouping draws the network', await page.locator('.title', { hasText: 'aws_vpc.main' }).count() >= 1);
    await page.fill('#search', 'route_table');
    await page.waitForFunction(() => document.querySelectorAll('.item.resource').length === 1);
    check('search', (await resources()).join() === 'aws_route_table.private', await resources());
    await page.fill('#search', '');
    await settled();

    // Secrets: redacted where the state marks them, withheld where it cannot.
    // The two states sort as lab, then old: the first marks its secrets,
    // the second was written by a version that could not.
    await page.goto(base + '#g=source&q=random_password');
    await page.reload();
    await page.waitForSelector('.item.resource');
    await page.locator('.item.resource').nth(0).click();
    await page.waitForSelector('#panel pre', { state: 'attached' });
    const marked = (await page.innerText('#panel')) + (await page.locator('#panel pre').textContent());
    check('a marked state shows its attributes, redacted', marked.includes('"length": 12') && marked.includes('"result": "(sensitive)"'), marked);
    await page.locator('.item.resource').nth(1).click();
    await page.waitForFunction(() => document.getElementById('panel').innerText.includes('Withheld'));
    const unmarked = await page.innerText('#panel');
    check('an unmarked state has its attributes withheld', (await page.locator('#panel pre').count()) === 0 && unmarked.includes('show -json'));
    check('no secret reaches the page', !/hunter2|4@Sze931N|8%exVpqbOs|\$2a\$10\$/.test(marked + unmarked + (await page.content())));

    // What a listing of the cloud found beyond the state, and what drifted.
    await page.goto(base + '#g=account%2Cregion%2Cnetwork&st=unmanaged');
    await page.reload();
    await page.waitForFunction(() => document.querySelectorAll('.item.resource').length === 2);
    check('resources found only in the cloud are drawn as unmanaged', (await page.locator('.item.resource.unmanaged').count()) === 2);
    check('a group says how many of its resources are unmanaged', (await page.locator('.group > .title small').first().innerText()).includes('2 unmanaged'));
    await page.locator('.item.resource.unmanaged').first().click();
    await page.waitForSelector('#panel h2');
    const unmanagedPanel = await page.innerText('#panel');
    check('the panel names the cloud ID and withholds the listing\'s attributes',
      unmanagedPanel.includes('arn:aws:ec2:us-east-1:123456789012:subnet/') && unmanagedPanel.includes('Withheld') && !unmanagedPanel.includes('cidr_block'), unmanagedPanel);
    await page.goto(base + '#g=source&st=drifted');
    await page.reload();
    await page.waitForFunction(() => document.querySelectorAll('.item.resource').length === 1);
    check('a resource deleted outside Terraform is drawn as drifted', (await classOfItem(page, 'local_file.removed')).includes('drifted'));

    // A plan: what it will change is marked, and can be filtered on.
    await page.goto(base + '#g=source');
    await page.reload();
    await page.waitForSelector('.item');
    check('a plan offers a filter on its changes', await page.locator('#change').isVisible());
    await page.selectOption('#change', 'any');
    await page.waitForFunction(() => document.querySelectorAll('.item.resource').length === 6);
    const classOf = async (text) => (await item(text).getAttribute('class')) || '';
    check('a resource the plan creates is marked', (await classOf('terraform_data.create')).includes('change-create'));
    check('a resource the plan deletes is marked, and as gone from the code',
      /change-delete/.test(await classOf('terraform_data.delete')) && /not-in-code/.test(await classOf('terraform_data.delete')), await classOf('terraform_data.delete'));
    await item('terraform_data.create').click();
    await page.waitForFunction(() => document.querySelector('#panel h2')?.textContent === 'terraform_data.create');
    const planned = await page.innerText('#panel');
    check('the panel says what is planned', planned.includes('The plan will create this'));
    check('the panel says which argument refers to what', planned.includes('Refers to') && /input\s*→\s*module\.net\.terraform_data\.subnet\s*through module\.net\.first/.test(planned), planned);
    check('the plan\'s variables stay out of the page', !(await page.content()).includes('hunter2'));
    await page.selectOption('#change', '');
    await page.keyboard.press('Escape');

    // The item budget: opening a fourth crowded block closes the oldest.
    await page.goto(base + '#g=source&q=test_thing');
    await page.reload();
    await page.waitForSelector('.item.collapsed');
    for (const name of ['test_thing.a', 'test_thing.b', 'test_thing.c', 'test_thing.d']) {
      await item(name).click();
      await page.waitForFunction((n) => [...document.querySelectorAll('.group > .title')].some((e) => e.textContent.startsWith(n)), name);
      await settled();
    }
    const shown = await page.locator('[data-id]').count();
    const open = await page.locator('.group > .title').allInnerTexts();
    check('the view stays within the budget', shown <= 300, shown);
    check('the oldest group was closed, the newest kept', !open.some((t) => t.startsWith('test_thing.a')) && open.some((t) => t.startsWith('test_thing.d')), open);
    check('the page says it closed a group', (await page.innerText('#notice')).includes('Closed 1 other group'), await page.innerText('#notice'));

    // The address bar restores the view.
    const url = page.url();
    await page.goto('about:blank');
    await page.goto(url);
    await page.waitForSelector('.group > .title');
    const restored = await page.locator('.group > .title').allInnerTexts();
    check('the address bar restores the view', restored.join() === open.join(), restored);

    // With Azure resources loaded, the menu offers to group by resource
    // group, and a machine sits in the subnet its network interface is in.
    await page.goto(base + '#q=azurerm');
    await page.reload();
    await page.waitForSelector('.item');
    const offered = await page.locator('#group-by option').allInnerTexts();
    check('grouping by resource group is offered', offered.includes('Account, resource group, network'), offered);
    await page.selectOption('#group-by', 'account,resource_group,network');
    await page.waitForSelector('.group > .title:has-text("rg-app")');
    const azure = await page.locator('.group > .title').allInnerTexts();
    check('resource groups are drawn', azure.some((t) => t.startsWith('rg-app')) && azure.some((t) => t.startsWith('rg-shared')), azure);
    await page.locator('.item.resource', { hasText: 'azurerm_linux_virtual_machine.web' }).first().click();
    await page.waitForSelector('#panel h2');
    const machine = await page.innerText('#panel');
    check('the panel names the resource group', machine.includes('Resource group') && machine.includes('rg-app'), machine);

    // A state that changes on disk is picked up.
    await page.goto(base + '#g=source');
    await page.reload();
    await page.waitForSelector('.item');
    fs.copyFileSync(path.join(fixtures, 'raw.tfstate'), net);
    const future = new Date(Date.now() + 5000);
    fs.utimesSync(net, future, future);
    await page.waitForFunction(() => [...document.querySelectorAll('.item')].some((e) => e.textContent.includes('data.aws_availability_zones.available')), null, { timeout: 15000 })
      .then(() => check('a changed state file is reloaded', true), () => check('a changed state file is reloaded', false));

    check('no errors in the page', errors.length === 0, errors);
  } finally {
    await browser.close();
    server.kill();
    fs.rmSync(dir, { recursive: true, force: true });
  }
  if (failures) {
    console.log(`${failures} check(s) failed`);
    process.exit(1);
  }
  console.log('all checks passed');
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
