import test from 'node:test';
import assert from 'node:assert/strict';
import { renderPagination } from '../js/pagination.js';

function makeDom() {
    const container = { innerHTML: '', children: [], append(...nodes) { this.children.push(...nodes); } };
    const byId = new Map([['pg', container]]);
    global.document = {
        getElementById: (id) => byId.get(id) || null,
        createElement(tagName) {
            const listeners = new Map();
            return {
                tagName,
                className: '',
                textContent: '',
                value: '',
                min: '',
                max: '',
                title: '',
                type: '',
                disabled: false,
                onclick: null,
                children: [],
                setAttribute() {},
                append(...nodes) { this.children.push(...nodes); },
                addEventListener(type, fn) { listeners.set(type, fn); },
                fire(type, ev) { const fn = listeners.get(type); if (fn) fn(ev); },
            };
        },
    };
    return container;
}

function render(total = 1000, pageSize = 20, page = 1) {
    const container = makeDom();
    const pageState = { page, pageSize, total };
    let reloads = 0;
    renderPagination('pg', pageState, () => reloads++);
    const jump = container.children.find((c) => c.className === 'pg-jump');
    assert.ok(jump, 'jump span rendered');
    const input = jump.children.find((c) => c.tagName === 'input');
    const go = jump.children.find((c) => c.tagName === 'button' && c.textContent === 'Go');
    return { container, pageState, input, go, reloads: () => reloads };
}

test('single page hides the whole bar', () => {
    const container = makeDom();
    renderPagination('pg', { page: 1, pageSize: 20, total: 15 }, () => { throw new Error('no reload'); });
    assert.equal(container.innerHTML, '');
});

test('Go click survives the blur race (the bug report case)', () => {
    // Real browsers blur the input BEFORE the button click fires, and the
    // cosmetic blur handler resets input.value to the current page. Reading
    // the live input value in the click handler then sees the RESET value and
    // the jump silently does nothing. The handler must use the last typed
    // value instead.
    const { pageState, input, go, reloads } = render(1000, 20, 1);
    input.value = '7';
    input.fire('input');      // user types 7
    input.fire('blur');       // mousedown on Go blurs first; prod resets display
    assert.equal(input.value, '1', 'blur reset must have run before the click');
    go.onclick();             // click arrives after the blur
    assert.equal(pageState.page, 7);
    assert.equal(reloads(), 1);
});

test('Enter jumps without any blur', () => {
    const { pageState, input, reloads } = render(1000, 20, 1);
    input.value = '3';
    input.fire('input');
    let prevented = false;
    input.fire('keydown', { key: 'Enter', preventDefault() { prevented = true; } });
    assert.ok(prevented);
    assert.equal(pageState.page, 3);
    assert.equal(reloads(), 1);
});

test('out-of-range input snaps to the nearest bound', () => {
    const { pageState, input, go, reloads } = render(1000, 20, 1); // 50 pages
    input.value = '999';
    input.fire('input');
    go.onclick();
    assert.equal(pageState.page, 50);
    assert.equal(reloads(), 1);

    const second = render(1000, 20, 50);
    second.input.value = '0';
    second.input.fire('input');
    second.go.onclick();
    assert.equal(second.pageState.page, 1);
});

test('invalid input resets the box and never reloads', () => {
    const { pageState, input, go, reloads } = render(1000, 20, 4);
    input.value = 'abc';
    input.fire('input');
    go.onclick();
    assert.equal(pageState.page, 4);
    assert.equal(reloads(), 0);
    assert.equal(input.value, '4');
});

test('jumping to the current page is a no-op', () => {
    const { pageState, input, go, reloads } = render(1000, 20, 4);
    input.value = '4';
    input.fire('input');
    go.onclick();
    assert.equal(pageState.page, 4);
    assert.equal(reloads(), 0);
});

test('Prev/Next still work and are bounded', () => {
    const { container, pageState, reloads } = render(1000, 20, 2);
    const prev = container.children.find((c) => c.tagName === 'button' && c.textContent === '\u2039 Prev');
    const next = container.children.find((c) => c.tagName === 'button' && c.textContent === 'Next \u203a');
    prev.onclick();
    assert.equal(pageState.page, 1);
    next.onclick(); next.onclick();
    assert.equal(pageState.page, 3);
    assert.equal(reloads(), 3);
});
