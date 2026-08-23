import test from 'node:test';
import assert from 'node:assert/strict';
import { activatePanel, initPanelTabs } from '../js/panel-tabs.js';

function fakeClassList(initial = []) {
    const values = new Set(initial);
    return {
        add(name) { values.add(name); },
        remove(name) { values.delete(name); },
        toggle(name, enabled) {
            if (enabled) values.add(name);
            else values.delete(name);
        },
        contains(name) { return values.has(name); },
    };
}

function fakeButton(panel, active = false) {
    let click = () => {};
    return {
        dataset: { panel },
        classList: fakeClassList(active ? ['active'] : []),
        addEventListener(name, handler) {
            if (name === 'click') click = handler;
        },
        click() { click(); },
    };
}

function installPanelDOM() {
    const names = ['daily', 'sessions', 'requests', 'rate'];
    const buttons = names.map((name, index) => fakeButton(name, index === 0));
    const panels = names.map((name, index) => ({
        id: 'panel-' + name,
        classList: fakeClassList(index === 0 ? ['active'] : []),
    }));
    const requestFilter = { classList: fakeClassList() };
    const byId = new Map(panels.map((panel) => [panel.id, panel]));
    byId.set('request-filter', requestFilter);
    const doc = {
        querySelectorAll(selector) {
            if (selector === '.panel-btn') return buttons;
            if (selector === '.panel') return panels;
            return [];
        },
        getElementById(id) { return byId.get(id) || null; },
    };
    return { names, buttons, panels, requestFilter, doc };
}

test('every primary panel refreshes after it becomes active', () => {
    const dom = installPanelDOM();
    const activated = [];

    initPanelTabs({
        doc: dom.doc,
        onActivate(panelName) {
            const index = dom.names.indexOf(panelName);
            assert.equal(dom.buttons[index].classList.contains('active'), true);
            assert.equal(dom.panels[index].classList.contains('active'), true);
            activated.push(panelName);
        },
    });

    dom.buttons.forEach((button, index) => {
        button.click();
        assert.deepEqual(
            dom.buttons.map((item) => item.classList.contains('active')),
            dom.names.map((_, itemIndex) => itemIndex === index),
        );
        assert.deepEqual(
            dom.panels.map((item) => item.classList.contains('active')),
            dom.names.map((_, itemIndex) => itemIndex === index),
        );
        assert.equal(
            dom.requestFilter.classList.contains('visible'),
            dom.names[index] === 'requests',
        );
    });

    assert.deepEqual(activated, dom.names);
});

test('programmatic panel redirect can switch without starting a refresh', () => {
    const dom = installPanelDOM();
    let activations = 0;

    const changed = activatePanel('daily', {
        doc: dom.doc,
        notify: false,
        onActivate: () => { activations += 1; },
    });

    assert.equal(changed, true);
    assert.equal(activations, 0);
    assert.equal(dom.panels[0].classList.contains('active'), true);
});

test('a tab without a matching panel does not disturb the active view', () => {
    const dom = installPanelDOM();
    const missing = fakeButton('missing');
    dom.buttons.push(missing);
    let activations = 0;

    initPanelTabs({ doc: dom.doc, onActivate: () => { activations += 1; } });
    missing.click();

    assert.equal(dom.buttons[0].classList.contains('active'), true);
    assert.equal(dom.panels[0].classList.contains('active'), true);
    assert.equal(activations, 0);
});
