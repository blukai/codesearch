function enabledSourceFileNav() {
	const matchedLineEls = Array.from(document.querySelectorAll(`[data-matched="true"]`));
	if (matchedLineEls.length === 0) {
		return;
	}

	const targetedLineNoEl = document.getElementById("targeted-line-no");
	const toPrevMatchButton = document.getElementById("to-prev-match");
	const toNextMatchButton = document.getElementById("to-next-match");

	function findTargetedLineEl() {
		const targetId = location.hash.substring(1);
		return matchedLineEls.find((el) => el.id == targetId);
	}

	function updateTargetedMatchNo(idx) {
		const targetedLineElIdx = idx ?? matchedLineEls.indexOf(findTargetedLineEl());
		targetedLineNoEl.innerText = targetedLineElIdx + 1;
	}

	function updateJumpButtonsDisabledState() {
		const targetedLineElIdx = matchedLineEls.indexOf(findTargetedLineEl());
		toPrevMatchButton.disabled = targetedLineElIdx - 1 < 0;
		toNextMatchButton.disabled = targetedLineElIdx + 1 > matchedLineEls.length - 1;
	}

	function wrapAround(max, n) {
		return n >= 0 ? n % max : (n % max + max) % max;
	}

	function jumpToMatchAtIdx(idx) {
		location.hash = matchedLineEls[idx].id;
		updateTargetedMatchNo(idx);
		updateJumpButtonsDisabledState();
	}

	updateTargetedMatchNo();
	updateJumpButtonsDisabledState();

	toPrevMatchButton.onclick = () => {
		const targetedLineElIdx = matchedLineEls.indexOf(findTargetedLineEl());
		const newIdx = wrapAround(matchedLineEls.length, targetedLineElIdx - 1);
		jumpToMatchAtIdx(newIdx)
	};
	toNextMatchButton.onclick = () => {
		const targetedLineElIdx = matchedLineEls.indexOf(findTargetedLineEl());
		const newIdx = wrapAround(matchedLineEls.length, targetedLineElIdx + 1);
		jumpToMatchAtIdx(newIdx);
	};
}
enabledSourceFileNav();
